//go:build ignore

// Command fake serves the three APIs node-tracker talks to, for the demo GIF:
// bitcoind's getblockchaininfo, Esplora's tip height and Telegram's
// sendMessage. The node is stuck at -node while the tip is at -tip. Each
// Telegram message is printed to stdout. Loopback only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

func main() {
	node := flag.Int("node", 974079, "node height")
	tip := flag.Int("tip", 974085, "chain tip height")
	addrs := flag.String("listen", "127.0.0.1:8332,127.0.0.1:3000,127.0.0.1:8081", "comma-separated addresses")
	flag.Parse()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			fmt.Fprintf(w, `{"result":{"blocks":%d,"headers":%d},"error":null,"id":1}`, *node, *node)
		case r.URL.Path == "/api/blocks/tip/height":
			fmt.Fprint(w, *tip)
		case strings.HasPrefix(r.URL.Path, "/bot") && strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var m struct{ Text string }
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				http.Error(w, `{"ok":false,"description":"bad json"}`, http.StatusBadRequest)
				return
			}
			fmt.Printf("telegram: %q\n", m.Text)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	})
	list := strings.Split(*addrs, ",")
	for _, a := range list[1:] {
		go func() { log.Fatal(http.ListenAndServe(a, h)) }()
	}
	log.Fatal(http.ListenAndServe(list[0], h))
}
