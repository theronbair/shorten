package main

import (
   "fmt"
   "log"
   "time"
   "strings"
   "math/rand"
   "net/http"
   "github.com/gorilla/mux"
   "github.com/rs/cors"
   "github.com/pborman/getopt/v2"
)

type Options struct {
   misc struct {
      maxURLs int
      serverBase string
   }
   behavior struct {
      shortLength int
   }
}

type URLPair struct {
   URL string
   Short string
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
   opts.misc.maxURLs = 1000
   opts.misc.serverBase = "http://localhost:8000"
   opts.behavior.shortLength = 8

   go insertUrl(uc)

   getopt.Flag(&opts.misc.maxURLs, 'n', "maximum URLs to store")
   getopt.Flag(&opts.misc.serverBase, 'b', "base URL for server")
   getopt.Flag(&opts.behavior.shortLength, 'l', "max length of shortened URL tag")
   getopt.Parse()

   r := mux.NewRouter().SkipClean(true)
   c := cors.New(cors.Options{
      AllowedOrigins: []string{"*"},
      AllowedMethods: []string{"GET", "POST"},
   })

   api := r.PathPrefix("/api/v1").Subrouter().StrictSlash(false)
   api.HandleFunc("/shorten/{url:.+}", shortenURL).Methods("POST")
   api.HandleFunc("/lookup/{id:.+}", lookupURL).Methods("GET")

   r.PathPrefix("/{short:.+}").HandlerFunc(returnRedirect).Methods("GET")

   log.Fatal(http.ListenAndServe("0.0.0.0:8000", c.Handler(r)))
}

func returnRedirect(w http.ResponseWriter, req *http.Request) {
   muxVars := mux.Vars(req)
   theShort := muxVars["short"]
   if s, s_exists := shorts[theShort]; s_exists {
      http.Redirect(w, req, s, http.StatusMovedPermanently)
   } else {
      http.Error(w, "Short Identifier Not Found", 404)
   }
}

// drillURL:  proceed through a chain of redirects until we get to the 'real' canonical URL, which is what we'll store
func drillURL(url string) (string, int) {
   req, err := http.NewRequest("GET", url, nil)
   if err != nil {
      panic(err)
   }
   client := new(http.Client)

   response, err := client.Do(req)

   if err == nil {
      urlString := response.Request.URL.String() // url_loc.String()
      if ( response.StatusCode >= 300 && response.StatusCode <= 399 ) {
         return drillURL(urlString)
      } 
      if ( response.StatusCode >= 200 && response.StatusCode <= 299 ) {
         return urlString, response.StatusCode
      } 
      return "", 404
   } else {
      fmt.Println(err.Error())
      return "", 404
   }
}

func insertUrl(urlchan chan URLPair) {
   for up := range urlchan {
      for u := range urls {
         if ( len(urls) < opts.misc.maxURLs ) {
            break
         }
         delete(urls, u)
      }
   
      for s := range shorts {
         if ( len(shorts) < opts.misc.maxURLs ) {
            break
         }
         delete(shorts, s)
      }
      urls[up.URL] = up.Short
      shorts[up.Short] = up.URL
   }
}
   
func shortenURL(w http.ResponseWriter, req *http.Request) {
   var up URLPair
   var theShort string
   // dirty trick - because the router is apparently stripping query strings, we just ignore the "URL" variable and 
   // strip the prefix from the string using TrimPrefix

   urlstr := strings.TrimPrefix(req.URL.String(), "/api/v1/shorten/")
   canonicalURL, status := drillURL(urlstr)

   if s, s_exists := urls[canonicalURL]; s_exists {
      theShort = s
   } else {
      theShort = randomString(opts.behavior.shortLength)
      up.Short = theShort
      up.URL = canonicalURL
      uc <- up
   }

   if status == 404 {
      http.Error(w, "URL Not Found", 404)
   } else {
      fmt.Fprintf(w, opts.misc.serverBase + "/" + theShort)
   }
}

func lookupURL(w http.ResponseWriter, req *http.Request) {
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

