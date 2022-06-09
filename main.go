package main

import (
   "fmt"
   "log"
   "time"
   "strings"
   "strconv"
   "context"
   "math/rand"
   "net/http"
   "net/url"
   "github.com/gorilla/mux"
   "github.com/rs/cors"
   "github.com/pborman/getopt/v2"
   sak "github.com/theronbair/sak"
   spew "github.com/davecgh/go-spew/spew"
)

type Options struct {
   listenAddr string
   listenPort int
   misc struct {
      maxURLs int
      serverBase string
   }
   behavior struct {
      shortLength int
      timeoutMs int64
   }
}

type URLPair struct {
   URL string
   Short string
   Delete bool
}

var (
   urls map[string]string
   shorts map[string]string
   uc chan URLPair
   opts = Options{}
)

func main() {
   now := time.Now()
   rand.Seed(now.UnixNano())

   urls = make(map[string]string)
   shorts = make(map[string]string)
   uc = make(chan URLPair)

   // set sane defaults
   opts.listenAddr = "0.0.0.0"
   opts.listenPort = 8000
   opts.misc.maxURLs = 1000
   opts.misc.serverBase = "http://" + opts.listenAddr + ":" + strconv.Itoa(opts.listenPort) 
   opts.behavior.shortLength = 8

   _ = getopt.Counter('v', "increase verbosity (can be repeated)")
   getopt.Flag(&opts.listenAddr, 'L', "interface address to listen to (0.0.0.0 = all interfaces)")
   getopt.Flag(&opts.listenPort, 'P', "port to listen to")
   getopt.Flag(&opts.misc.maxURLs, 'n', "maximum URLs to store")
   getopt.Flag(&opts.misc.serverBase, 'b', "base URL for server")
   getopt.Flag(&opts.behavior.shortLength, 'l', "max length of shortened URL tag")
   getopt.Flag(&opts.behavior.timeoutMs, 't', "timeout for recursive URL resolution")
   getopt.Parse()

   sak.Opts.DebugLevel = getopt.GetCount('v')
   sak.LOG(2, sak.L{F: "init"}, "parsed options: ", spew.Sdump(opts))

   sak.LOG(1, sak.L{F: "init"}, "initializing router")
   r := mux.NewRouter().SkipClean(true)
   c := cors.New(cors.Options{
      AllowedOrigins: []string{"*"},
      AllowedMethods: []string{"GET", "POST"},
   })

   go manageURL(uc)

   api := r.PathPrefix("/api/v1").Subrouter().StrictSlash(false)
   api.HandleFunc("/shorten/{url:.+}", shortenURL).Methods("POST")
   api.HandleFunc("/lookup/{id:.+}", lookupURL).Methods("GET")

   r.PathPrefix("/{short:.+}").HandlerFunc(returnRedirect).Methods("GET")

   sak.LOG(1, sak.L{F: "http"}, "starting HTTP server:")
   log.Fatal(http.ListenAndServe(opts.listenAddr + ":" + strconv.Itoa(opts.listenPort), c.Handler(r)))
}

func returnRedirect(w http.ResponseWriter, req *http.Request) {
   sak.LOG(1, sak.L{F: "returnRedirect"}, "entered function")
   sak.LOG(3, sak.L{F: "returnRedirect"}, "request:", spew.Sdump(req))
   
   muxVars := mux.Vars(req)
   theShort := muxVars["short"]
   if s, s_exists := shorts[theShort]; s_exists {
      http.Redirect(w, req, s, http.StatusMovedPermanently)
   } else {
      http.Error(w, "Short Identifier Not Found", 404)
   }
}

// drillURL:  proceed through a chain of redirects until we get to the 'real' canonical URL, which is what we'll store
// modification:  memoize the same short for every URL traversed
func drillURL(ctx context.Context, propPair URLPair, uc chan URLPair) (actualPair URLPair, responseCode int) {
   sak.LOG(1, sak.L{F: "drillURL"}, "entered function")
   sak.LOG(2, sak.L{F: "drillURL"}, fmt.Sprintf("called with URL %s, shortcode %s", propPair.URL, propPair.Short))
   // we'll pretend first that we haven't seen it
   actualPair = propPair
   responseCode = 200

   // shortcut; if this URL is known, look up its 'short', then look up the URL for that short, which should be the canonical URL
   if s, s_exists := urls[propPair.URL]; s_exists {
      actualPair.URL = shorts[s]
      actualPair.Short = s
      uc <- actualPair
      sak.LOG(1, sak.L{F: "drillURL"}, fmt.Sprintf("memoized URL %s found, discarding proposed shortcode %s, returning existing shortcode %s", actualPair.URL, propPair.Short, actualPair.Short))
      return
   }

   // otherwise, fetch it and see if it redirects
   // but push it first
   uc <- actualPair
   req, err := http.NewRequestWithContext(ctx, "GET", propPair.URL, nil)
   if err != nil {
      panic(err)
   }
   client := new(http.Client)

   sak.LOG(1, sak.L{F: "drillURL"}, fmt.Sprintf("getting URL %s", propPair.URL))
   response, err := client.Do(req)
   sak.LOG(2, sak.L{F: "drillURL"}, "error:", spew.Sdump(err))
   sak.LOG(3, sak.L{F: "drillURL"}, "URL response:", spew.Sdump(response))

   if err == nil { // nothing went wrong!
      actualPair.URL = response.Request.URL.String() 
      sak.LOG(2, sak.L{F: "drillURL"}, "got URL: ", actualPair.URL)
      if ( response.StatusCode >= 300 && response.StatusCode <= 399 ) {
         sak.LOG(1, sak.L{F: "drillURL"}, fmt.Sprintf("URL %s redirected to %s, following", propPair.URL, actualPair.URL))
         // dump this into the channel and dig deeper
         sak.LOG(1, sak.L{F: "drillURL"}, "dumping URL pair to channel")
         sak.LOG(2, sak.L{F: "drillURL"}, "URL/short pair:", spew.Sdump(actualPair))
         sak.LOG(1, sak.L{F: "drillURL"}, "calling drillURL again with new URL")
         actualPair, responseCode = drillURL(ctx, actualPair, uc)
      } 
      if ( response.StatusCode >= 200 && response.StatusCode <= 299 ) {
         // we found it - the proposed short hasn't been seen, so it is now
         actualPair.Short = propPair.Short
         responseCode = response.StatusCode
         sak.LOG(1, sak.L{F: "drillURL"}, fmt.Sprintf("URL search terminated!  URL %s, shortcode %s, response code %d", actualPair.URL, actualPair.Short, responseCode))
      } 
      sak.LOG(2, sak.L{F: "drillURL"}, fmt.Sprintf("pushing URL %s / shortcode %s to channel", actualPair.URL, actualPair.Short))
      uc <- actualPair
   } else {
      switch ctx.Err() {
         case context.DeadlineExceeded: // this is probably fine, just return what we have
            uc <- propPair
            responseCode = 234                              // we'll return "234 I'm Lying Right Now But It's OK"
            actualPair = propPair
            actualPair.URL = err.(*url.Error).URL           // dirty hack:  extract the URL from the error message, since where it terminated may be different
            sak.LOG(2, sak.L{F: "drillURL"}, fmt.Sprintf("restarting drillURL in background context with URL %s, short %s", actualPair.URL, actualPair.Short))
            go drillURL(context.Background(), actualPair, uc) // but we're going to keep digging to find the URLs anyway, just in the background
            break

         default: // something bad happened, barf
            sak.LOG(0, sak.L{F: "drillURL"}, "ERROR: ", err.Error())
            actualPair = propPair
            actualPair.Delete = true
            responseCode = 404
      }
   }

   sak.LOG(1, sak.L{F: "drillURL"}, fmt.Sprintf("returning:  URL %s, shortcode %s, responseCode %d", actualPair.URL, actualPair.Short, responseCode))
   return
}

func manageURL(uc chan URLPair) {
   sak.LOG(1, sak.L{F: "manageURL"}, "entered function")
   for up := range uc {
      sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("got URL pair on channel:  URL %s, shortcode %s", up.URL, up.Short))
      if up.Delete {
         sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("delete flag set, deleting URL %s and short %s", up.URL, up.Short))
         delete(urls, up.URL)
         delete(shorts, up.Short)
      } else {
         sak.LOG(1, sak.L{F: "manageURL"}, "delete flag not set, inserting URL")
         sak.LOG(1, sak.L{F: "manageURL"}, "checking for space")

         for u := range urls {
            if ( len(urls) < opts.misc.maxURLs ) {
               sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("have enough space for URLs:  length %d, max %d", len(urls), opts.misc.maxURLs))
               break
            }
            sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("need space, forgetting URL %s", u))
            delete(urls, u)
         }
      
         for s := range shorts {
            if ( len(shorts) < opts.misc.maxURLs ) {
               sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("have enough space for shorts:  length %d, max %d", len(shorts), opts.misc.maxURLs))
               break
            }
            sak.LOG(1, sak.L{F: "manageURL"}, fmt.Sprintf("need space, forgetting shortcode %s", s))
            delete(shorts, s)
         }
         urls[up.URL] = up.Short
         shorts[up.Short] = up.URL
      }
   }
}
   
func shortenURL(w http.ResponseWriter, req *http.Request) {
   sak.LOG(1, sak.L{F: "shortenURL"}, "entered function")
   var up URLPair

   // dirty trick - because the router is apparently stripping query strings, we just ignore the "URL" variable and 
   // strip the prefix from the string using TrimPrefix

   up.URL = strings.TrimPrefix(req.URL.String(), "/api/v1/shorten/")
   up.Short = randomString(opts.behavior.shortLength)

   sak.LOG(1, sak.L{F: "shortenURL"}, fmt.Sprintf("testing shortcode %s for URL %s", up.Short, up.URL))
   ctx, timesup := context.WithTimeout(context.Background(), time.Duration(opts.behavior.timeoutMs) * time.Millisecond)
   up, status := drillURL(ctx, up, uc)
   defer timesup()

   select {
      case <-time.After(time.Duration(opts.behavior.timeoutMs) * time.Millisecond):
            if status == 404 {
               http.Error(w, "URL Not Found", 404)
            } else {
               sak.LOG(1, sak.L{F: "shortenURL"}, fmt.Sprintf("done, returning URL: '%s/%s'", opts.misc.serverBase, up.Short))
               fmt.Fprintf(w, opts.misc.serverBase + "/" + up.Short)
            }
      case <-ctx.Done():
            if status == 404 {
               http.Error(w, "URL Not Found", 404)
            } else {
               sak.LOG(1, sak.L{F: "shortenURL"}, fmt.Sprintf("timed out, returning URL: '%s/%s'", opts.misc.serverBase, up.Short))
               fmt.Fprintf(w, opts.misc.serverBase + "/" + up.Short)
            }
   }
   uc <- up
}

func lookupURL(w http.ResponseWriter, req *http.Request) {
   sak.LOG(1, sak.L{F: "lookupURL"}, "entered function")
   muxVars := mux.Vars(req)
   theShort := muxVars["id"]

   if u, u_exists := shorts[theShort]; u_exists {
      fmt.Fprintf(w, u)
      return
   } else { 
      http.Error(w, "URL Not Found", 404)
   }
}


// helper functions
func randomString(n int) string {
   var letter = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

   b := make([]rune, n)
   for i := range b {
      b[i] = letter[rand.Intn(len(letter))]
   }
   return string(b)
}

func debugLogger(next http.Handler) http.Handler {
   return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      log.Println(r.RequestURI)
      next.ServeHTTP(w, r)
   })
}

