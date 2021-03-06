package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/miekg/dns"

	"gopkg.in/alecthomas/kingpin.v2"
)

type QueueItem struct {
	IP        net.IP
	ReverseIP string
	Blacklist string
	Error     error
	FQDN      string
	Response  []string
}

var (
	command string
	results []QueueItem
	hosts   []string

	// channels
	queue    chan QueueItem
	response chan QueueItem

	wg sync.WaitGroup
)

var (
	app     = kingpin.New(Name, Description)
	verbose = app.Flag("verbose", "Verbose mode.").Bool()
	version = app.Command("version", "Show version and terminate").Action(ShowVersion)

	nameserver = app.Flag("nameserver", "Name server to use").Default("8.8.8.8:53").TCP()
	queueSize  = app.Flag("queue", "How many request to process at one time").Default("15").Int()
	//	blacklistFile = app.Flag("blacklist", "A blacklist file to use").ExistingFile()

	checkIp = app.Command("ip", "Check IP against available blacklists")
	ip      = checkIp.Arg("ip-address", "IP address to check against blacklists.").Required().IP()
	//	ipBlacklistServer = checkIp.Arg("blacklist-server", "Blacklist server to check against").HintAction(GetBlacklistHosts).String()

	checkRange = app.Command("cidr", "Check CIDR against available blacklists")
	rangeCidr  = checkRange.Arg("cidr-address", "CIDR address to check against blacklists.").Required().String()
	//	rangeCidrBlacklistServer = checkRange.Arg("blacklist-server", "Blacklist server to check against").HintAction(GetBlacklistHosts).String()

	list = app.Command("list", "List available blacklists")
)

func ShowVersion(c *kingpin.ParseContext) error {
	fmt.Printf("%s version %s build %s (%s), built on %s, by %s\n", Name, BuildVersion, BuildHash, runtime.GOARCH, BuildDate, Maintainer)
	os.Exit(0)
	return nil
}

func init() {

	app.HelpFlag.Short('h')
	command = kingpin.MustParse(app.Parse(os.Args[1:]))

	queue = make(chan QueueItem, *queueSize)
	response = make(chan QueueItem)

	hosts = GetBlacklistHosts()

}

func main() {
	switch command {
	case "list":
		for _, blacklist := range hosts {
			fmt.Printf("%s\n", blacklist)
		}
	case "cidr":
		ips, err := Hosts(*rangeCidr)
		if err != nil {
			fmt.Printf("%v", err)
			return
		}
		wg.Add(len(ips) * len(hosts))
		go ProcessQueue()
		go AddQueueItemsToQueue(ips)
		wg.Wait()
	case "ip":
		ips := []net.IP{*ip}
		wg.Add(len(ips) * len(hosts))
		go ProcessQueue()
		go AddQueueItemsToQueue(ips)
		wg.Wait()
	}
}

func ProcessQueue() {

	for {
		select {
		case qi := <-queue:
			go CheckIfBlacklisted(response, qi.IP, qi.Blacklist)
		case qr := <-response:
			if len(qr.Response) > 0 {
				fmt.Printf("%s blacklisted on %s with %s\n", qr.IP.String(), qr.Blacklist, strings.Join(qr.Response, ","))
			}
		}
	}

}

func AddQueueItemsToQueue(IPs []net.IP) {

	for _, ip := range IPs {
		for _, blacklist := range hosts {
			queue <- QueueItem{
				IP:        ip,
				Blacklist: blacklist,
			}
		}
	}

}

func CheckIfBlacklisted(channel chan<- QueueItem, IP net.IP, blacklist string) {

	defer wg.Done()

	client := new(dns.Client)

	qi := QueueItem{
		IP:        IP,
		ReverseIP: ReverseIP(IP.String()),
		Blacklist: blacklist,
		FQDN:      fmt.Sprintf("%s.%s.", ReverseIP(IP.String()), blacklist),
	}

	if *verbose {
		fmt.Printf("Checking %s\n", qi.FQDN)
	}

	m := new(dns.Msg)
	m.SetQuestion(qi.FQDN, dns.TypeA)
	m.RecursionDesired = true

	r, _, err := client.Exchange(m, (*nameserver).String())
	if err != nil {
		if *verbose {
			fmt.Printf("Failed to query: %v for %v on %v with query %v\n", err, qi.IP, qi.Blacklist, qi.FQDN)
		}
		qi.Error = err
		wg.Add(1)
		queue <- QueueItem{
			IP:        IP,
			Blacklist: blacklist,
		}
		return
	}

	if r.Rcode != dns.RcodeSuccess {
		qi.Error = errors.New(fmt.Sprintf("Recieved Rcode: %v is different from %v (RcodeSuccess) for %v", r.Rcode, dns.RcodeSuccess, qi.FQDN))
		if *verbose {
			fmt.Printf("%v\n", qi.Error)
		}
		return
	}

	resp := []string{}

	for _, a := range r.Answer {
		if rsp, ok := a.(*dns.A); ok {
			resp = append(resp, rsp.A.String())
		}
	}

	qi.Response = resp

	if *verbose {
		fmt.Printf("Successfully queried %v with %v response\n", qi.FQDN, qi.Response)
	}

	channel <- qi

}
