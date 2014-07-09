package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"code.google.com/p/gcfg"
	_ "github.com/go-sql-driver/mysql"

	"github.com/dbrower/disadis/auth"
	"github.com/dbrower/disadis/fedora"
)

type Reopener interface {
	Reopen()
}

type loginfo struct {
	name string
	f    *os.File
}

func NewReopener(filename string) *loginfo {
	return &loginfo{name: filename}
}

func (li *loginfo) Reopen() {
	if li.name == "" {
		return
	}
	if li.f != nil {
		log.Println("Reopening Log files")
	}
	newf, err := os.OpenFile(li.name, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(newf)
	if li.f != nil {
		li.f.Close()
	}
	li.f = newf
}

func signalHandler(sig <-chan os.Signal, logw Reopener) {
	for s := range sig {
		log.Println("---Got", s)
		switch s {
		case syscall.SIGUSR1:
			logw.Reopen()
		}
	}
}

type Config struct {
	General struct {
		Log_filename string
		Fedora_addr  string
		Admin        []string
	}
	Pubtkt struct {
		Key_file string
	}
	Rails struct {
		Secret   string
		Cookie   string
		Database string
	}
	Handler map[string]*struct {
		Port          string
		Auth          bool
		Versioned     bool
		Prefix        string
		Datastream    string
		Datastream_id []string
	}
}

func main() {
	var (
		logfilename string
		logw        Reopener
		pubtktKey   string
		fedoraAddr  string
		secret      string
		database    string
		cookieName  string
		configFile  string
		config      Config
	)

	flag.StringVar(&logfilename, "log", "", "name of log file. Defaults to stdout")
	flag.StringVar(&pubtktKey, "pubtkt-key", "",
		"filename of PEM encoded public key to use for pubtkt authentication")
	flag.StringVar(&fedoraAddr, "fedora", "",
		"url to use for fedora, includes username and password, if needed")
	flag.StringVar(&secret, "secret", "",
		"secret to use to verify rails 3 cookies")
	flag.StringVar(&database, "db", "",
		"path and credentials to access the user database (mysql). Needed if --secret is given")
	flag.StringVar(&cookieName, "cookie", "",
		"name of cookie holding the rails 3 session")
	flag.StringVar(&configFile, "config", "",
		"name of config file to use")

	flag.Parse()

	// the config file stuff was grafted onto the command line options
	// this should be made pretty
	if configFile != "" {
		err := gcfg.ReadFileInto(&config, configFile)
		if err != nil {
			log.Println(err)
		}
		logfilename = config.General.Log_filename
		fedoraAddr = config.General.Fedora_addr
		pubtktKey = config.Pubtkt.Key_file
		secret = config.Rails.Secret
		database = config.Rails.Database
		cookieName = config.Rails.Cookie
	}

	/* first set up the log file */
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	logw = NewReopener(logfilename)
	logw.Reopen()
	log.Println("-----Starting Server")

	/* set up signal handlers */
	sig := make(chan os.Signal, 5)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	go signalHandler(sig, logw)

	/* Now set up the handler chains */
	if fedoraAddr == "" {
		log.Printf("Error: Fedora address must be set. (--fedora <server addr>)")
		os.Exit(1)
	}
	fedora := fedora.NewRemote(fedoraAddr, "")
	ha := auth.NewHydraAuth(fedoraAddr, "")
	ha.Admin = config.General.Admin
	log.Println("Admin users:", ha.Admin)
	switch {
	case pubtktKey != "":
		log.Printf("Using pubtkt %s", pubtktKey)
		ha.CurrentUser = auth.NewPubtktAuthFromKeyFile(pubtktKey)
	case secret != "":
		log.Printf("Using Rails 3 cookies")
		if cookieName == "" {
			log.Printf("Warning: The name of the cookie holding the rails session is required (--cookie)")
			break
		}
		log.Printf("Cookie name '%s'", cookieName)
		if database == "" {
			log.Printf("Warning: A database (--db) is required to use rails cookies")
			break
		}
		db, err := sql.Open("mysql", database)
		if err != nil {
			log.Printf("Error opening database connection: %s", err)
			break
		}
		ha.CurrentUser = &auth.DeviseAuth{
			SecretBase: []byte(secret),
			CookieName: cookieName,
			Lookup:     &auth.DatabaseUser{Db: db},
		}
	default:
		log.Printf("Warning: No authorization method given.")
	}
	if ha.CurrentUser == nil {
		log.Printf("Warning: Only Allowing Public Access.")
	}
	if len(config.Handler) == 0 {
		log.Printf("No Handlers are defined. Exiting.")
		return
	}

	runHandlers(config, fedora, ha)
}

type handlerBootstrap struct {
	h    http.Handler
	name string
}

// runHandlers starts a listener for each port in its own goroutine
// and then waits for all of them to quit.
func runHandlers(config Config, fedora fedora.Fedora, auth *auth.HydraAuth) {
	var wg sync.WaitGroup
	portHandlers := make(map[string]*DsidMux)
	// first create the handlers
	for k, v := range config.Handler {
		h := &DownloadHandler{
			Fedora:    fedora,
			Ds:        v.Datastream,
			Versioned: v.Versioned,
			Prefix:    v.Prefix,
		}
		if v.Auth {
			h.Auth = auth
		}
		log.Printf("Handler %s (datastream %s, port %s, auth %v, dsid %v)",
			k,
			v.Datastream,
			v.Port,
			v.Auth,
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
