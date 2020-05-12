package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	gcfg "gopkg.in/gcfg.v1"

	"github.com/ndlib/disadis/fedora"
)

func signalHandler(sig <-chan os.Signal) {
	for s := range sig {
		log.Println("---Received signal", s)
		switch s {
		case syscall.SIGINT, syscall.SIGTERM:
			log.Println("Exiting")
			os.Exit(1)
		}

	}
}

// the structure of our configuration file.
type config struct {
	General struct {
		Fedora_addr string
		Bendo_token string
	}
	Handler map[string]*struct {
		Port          string
		Prefix        string
		Datastream    string
		Datastream_id []string
	}
}

func main() {
	var (
		fedoraAddr  string
		configFile  string
		config      config
		showVersion bool
	)

	flag.StringVar(&fedoraAddr, "fedora", "",
		"url to use for fedora, includes username and password, if needed")
	flag.StringVar(&configFile, "config", "",
		"name of config file to use")
	flag.BoolVar(&showVersion, "version", false, "Display the version and exit")

	flag.Parse()

	if showVersion {
		fmt.Printf("disadis version %s\n", Version)
		return
	}

	// the config file stuff was grafted onto the command line options
	// this should be made pretty
	if configFile != "" {
		err := gcfg.ReadFileInto(&config, configFile)
		if err != nil {
			log.Println(err)
		}
		fedoraAddr = config.General.Fedora_addr
	}

	/* first set up the log file */
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("-----Starting Disadis Server", Version)

	/* set up signal handlers */
	sig := make(chan os.Signal, 5)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go signalHandler(sig)

	/* Now set up the handler chains */
	if fedoraAddr == "" {
		log.Printf("Error: Fedora address must be set. (--fedora <server addr>)")
		os.Exit(1)
	}
	fedora := fedora.NewRemote(fedoraAddr, "")
	if config.General.Bendo_token != "" {
		log.Println("Bendo token supplied")
	}
	if len(config.Handler) == 0 {
		log.Printf("No Handlers are defined. Exiting.")
		return
	}

	runHandlers(config, fedora)
}

// runHandlers starts a listener for each port in its own goroutine
// and then waits for all of them to quit.
func runHandlers(config config, fedora fedora.Fedora) {
	var wg sync.WaitGroup
	portHandlers := make(map[string]*DsidMux)
	// first create the handlers
	for k, v := range config.Handler {
		h := &DownloadHandler{
			Fedora:     fedora,
			Ds:         v.Datastream,
			Prefix:     v.Prefix,
			BendoToken: config.General.Bendo_token,
		}
		log.Printf("Handler %s (datastream %s, port %s, dsid %v)",
			k,
			v.Datastream,
			v.Port,
			v.Datastream_id)
		mux, ok := portHandlers[v.Port]
		if !ok {
			mux = &DsidMux{}
			portHandlers[v.Port] = mux
		}
		// see http://golang.org/doc/faq#closures_and_goroutines
		k := k // make local ref to var for closure
		hh := http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				t := time.Now()
				realip := r.Header.Get("X-Real-IP")
				if realip == "" {
					realip = r.RemoteAddr
				}
				h.ServeHTTP(w, r)
				log.Printf("%s %s %s %s %v",
					k,
					realip,
					r.Method,
					r.RequestURI,
					time.Now().Sub(t))
			})
		if len(v.Datastream_id) == 0 {
			mux.DefaultHandler = hh
		}
		for _, name := range v.Datastream_id {
			if name == "default" {
				mux.DefaultHandler = hh
			} else {
				mux.AddHandler(name, hh)
			}
		}
	}
	// now start a goroutine for each port
	for port, h := range portHandlers {
		wg.Add(1)
		go http.ListenAndServe(":"+port, h)
	}
	// Listen on 6060 to get pprof output
	go http.ListenAndServe(":6060", nil)
	// We add things to the waitgroup, but never call wg.Done(). This will never return.
	wg.Wait()
}
