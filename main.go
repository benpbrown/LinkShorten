package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/mattn/go-sqlite3"
)

const DATABASE_FILENAME = "shortener.sqlite3"

func reverseByteSlice(s []byte) []byte {
	var t []byte
	for i := len(s) - 1; i >= 0; i-- {
		t = append(t, s[i])
	}
	return t
}

// http://stackoverflow.com/a/14238685
func CToGoString(c []byte) string {
	n := -1
	for i, b := range c {
		if b == 0 {
			break
		}
		n = i
	}
	return string(c[:n+1])
}

const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
const BASE = 62

// http://stackoverflow.com/a/742047
func stringFromId(num int64) string {
	var digits []byte
	for num > 0 {
		remainder := num % BASE
		digits = append(digits, byte(remainder))
		num /= BASE
	}

	digits = reverseByteSlice(digits)
	for i, v := range digits {
		digits[i] = ALPHABET[v]
	}

	return CToGoString(digits)
}

func IdFromString(s string) (int64, error) {
	var sum int64
	digits := []byte(s)
	digits = reverseByteSlice(digits)
	for i, v := range digits {
		index := strings.IndexByte(ALPHABET, v)
		if index >= len(ALPHABET) {
			return 0, errors.New("Invalid shortened URL")
		}
		sum += int64(index) * int64(math.Pow(BASE, float64(i)))
	}
	return sum, nil
}

func getShortUrlFromId(id int64, req *http.Request) string {
	/* Get a shortened URL from an id */

	// If we are behind a reverse proxy, get the protocol that the client
	// is connecting to
	protocol := req.Header.Get("X-Forwarded-Proto")

	// If the header isn't included, either the reverse proxy isn't sending it, or we are
	// hosting directly.
	if protocol == "" {
		if req.TLS != nil {
			protocol = "https"
		} else {
			protocol = "http"
		}
	}
	return protocol + "://" + req.Host + "/" + stringFromId(id)
}

func main() {
	var servePortNumber int
	flag.IntVar(&servePortNumber, "p", 8888, "Sets the port number that the HTTP server will listen on")
	flag.Parse()

	// Compile and cache the templates
	tmpl := template.Must(template.ParseGlob("templates/*.html"))

	db, err := sql.Open("sqlite3", DATABASE_FILENAME)
	if err != nil {
		log.Fatal(err)
	}
	// Ping actually writes the file to disk if it doesn't exist yet
	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sqlStmt := "CREATE TABLE IF NOT EXISTS urls ( id INTEGER NOT NULL PRIMARY KEY, url TEXT UNIQUE);"
	sqlStmt += `CREATE TABLE IF NOT EXISTS hits
	 		(
	 		id INTEGER NOT NULL PRIMARY KEY,
	 		url_id INTEGER NOT NULL,
	 		ip TEXT NOT NULL,
	 		access_time INTEGER NOT NULL,
	 		FOREIGN KEY(url_id) REFERENCES urls(id)
	 		)`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		log.Fatal(err)
	}

	var newLinkHandler = func(w http.ResponseWriter, req *http.Request) (int64, error) {
		if req.Method != "POST" {
			// User trying to GET the page
			return -1, errors.New("User did not POST to new link page. ")
		}
		req.ParseForm()
		var rawUrl = req.Form.Get("long_url")
		urlIsValid := govalidator.IsURL(rawUrl)
		if !urlIsValid {
			return -1, errors.New("User gave an invalid URL to new link page. ")
		}
		parsedUrl, err := url.Parse(rawUrl)
		if err != nil {
			// Malformed input from the user
			return -1, errors.New("User gave an invalid URL to new link page. ")
		}
		longUrl := parsedUrl.String()
		// db.Exec sanitizes our input for us if we use the question marks
		res, err := db.Exec("INSERT INTO urls(id, url) VALUES (?, ?)", nil, longUrl)

		var id int64
		if err != nil {
			// Cast to the library's error type
			trueErr, ok := err.(sqlite3.Error)
			var ExtendedCode sqlite3.ErrNoExtended
			if ok {
				ExtendedCode = trueErr.ExtendedCode
			}
			switch ExtendedCode {

			// If the URL isn't unique, we already have it in our database, so we can re-use
			// that id
			case sqlite3.ErrConstraintUnique:
				err = db.QueryRow("SELECT id FROM urls where url=?", longUrl).Scan(&id)
				if err != nil {
					log.Fatal(err)
				}
			default:
				log.Fatal(err)
			}
		} else {
			id, err = res.LastInsertId()
		}

		if err != nil {
			log.Fatal(err)
		}

		log.Printf("id=%v: url=%v --> short_url=%v\n", id, longUrl, stringFromId(id))
		// If we made it this far, no error
		return id, nil
	}

	http.HandleFunc("/api/newLink", func(w http.ResponseWriter, req *http.Request) {
		id, err := newLinkHandler(w, req)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		type ShortUrlLongUrl struct {
			ShortUrl string
			LongUrl  string
		}
		// ParseForm can be called multiple times without ill effect (idempotent)
		req.ParseForm()
		// No chance of error since we already parsed this is newLinkHandler
		longUrl, _ := url.Parse(req.Form.Get("long_url"))

		p := ShortUrlLongUrl{getShortUrlFromId(id, req), longUrl.String()}
		json.NewEncoder(w).Encode(p)
	})

	http.HandleFunc("/newLink", func(w http.ResponseWriter, req *http.Request) {
		id, err := newLinkHandler(w, req)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		// We can ignore the error since we set the value going into url.Parse
		// It is known that this value is valid and will not throw an error
		redirUrl, _ := url.Parse("/success")
		q := redirUrl.Query()
		q.Set("short", stringFromId(id))
		redirUrl.RawQuery = q.Encode()
		log.Println("Redirecting user to " + redirUrl.String())
		http.Redirect(w, req, redirUrl.String(), http.StatusSeeOther)
	})

	http.HandleFunc("/success", func(w http.ResponseWriter, req *http.Request) {
		short := req.URL.Query().Get("short")
		if strings.Compare(short, "") == 0 {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		id, err := IdFromString(short)
		if err != nil {
			http.NotFound(w, req)
			return
		}
		var url string
		err = db.QueryRow("SELECT url FROM urls WHERE id = ?", id).Scan(&url)
		if err != nil {
			http.NotFound(w, req)
			return
		}
		shortUrl := getShortUrlFromId(id, req)
		type ShortLongUrls struct {
			Shortened_url string
			Original_url  string
		}
		shortlongurls := ShortLongUrls{shortUrl, url}
		err = tmpl.ExecuteTemplate(w, "successPage", shortlongurls)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/" {
			err := tmpl.ExecuteTemplate(w, "indexPage", nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// URL.path comes in with a prefixed slash e.g. host:port/123 --> "/123"
		// We just want the ID, so remove the slash prefix

		// TODO: Currently also trimming the suffix to be flexible
		// e.g. /123/ is valid. Is this behaviour desired?
		path := req.URL.Path
		path = strings.TrimPrefix(path, "/")
		path = strings.TrimSuffix(path, "/")

		// Currently only return one type of error
		id, err := IdFromString(path)
		if err != nil {
			http.NotFound(w, req)
			return
		}

		var url string
		err = db.QueryRow("SELECT url FROM urls WHERE id = ?", id).Scan(&url)
		if err != nil {
			http.NotFound(w, req)
			return
		}

		// In case we are behind a proxy
		ipAddress := req.Header.Get("X-Real-IP")
		if ipAddress == "" {
			// Sometimes proxies use this
			ipAddress = req.Header.Get("X-Forwarded-For")
			if ipAddress == "" {
				// Okay, we're probably the host, let's pull the IP from the request
				ipAddress = req.RemoteAddr
			}
		}

		// If we've reached this point, then the id is valid
		_, err = db.Exec("INSERT INTO hits(id, url_id, ip, access_time) VALUES (?, ?, ?, ?)", nil, id, ipAddress, time.Now().Unix())
		if err != nil {
			// This really shouldn't fail, but if it does, we should still let the
			// user redirect.
			log.Printf("Error updating hit counts for id=%v", id)
		}
		http.Redirect(w, req, url, http.StatusMovedPermanently)
	})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", servePortNumber), nil))
}
