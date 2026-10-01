// certctl provisions local certificates; it never installs system trust.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/radityama/portway/internal/certificates"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "init" && os.Args[1] != "issue") {
		fmt.Fprintln(os.Stderr, "usage: portway-cert init|issue --dir <private directory> [--hosts name,*.base --days 30 --renew-before 168h]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("certificates", flag.ContinueOnError)
	dir := flags.String("dir", ".tmp/certificates", "private local CA/bundle directory")
	hosts := flags.String("hosts", "", "comma-separated certificate DNS names")
	days := flags.Int("days", 30, "leaf lifetime in days (1..90)")
	renewal := flags.Duration("renew-before", 7*24*time.Hour, "renew when remaining lifetime is at most this duration")
	if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
		os.Exit(2)
	}
	if os.Args[1] == "init" {
		if *hosts != "" || certificates.InitCA(*dir, time.Now()) != nil {
			fmt.Fprintln(os.Stderr, "local CA initialization failed")
			os.Exit(1)
		}
		fmt.Println("Local CA ready; trust its ca.pem explicitly on development clients.")
		return
	}
	entry, err := certificates.Issue(*dir, strings.Split(*hosts, ","), *days, *renewal, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "certificate issuance failed")
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(entry)
}
