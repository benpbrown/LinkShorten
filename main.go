package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"errors"
	"github.com/mattn/go-sqlite3"
	"html"
	"math"
	"net/url"
	"strings"
)

const SUCCESS_PAGE = `
<!DOCTYPE html>
<html lang="en">
<head>
  <title>Your Link was Shortened</title>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="//maxcdn.bootstrapcdn.com/bootstrap/3.3.6/css/bootstrap.min.css">
  <script src="//ajax.googleapis.com/ajax/libs/jquery/1.12.0/jquery.min.js"></script>
  <script src="//maxcdn.bootstrapcdn.com/bootstrap/3.3.6/js/bootstrap.min.js"></script>
</head>
<body>

<div class="container-fluid" style="text-align:center;">
  <div class="jumbotron">
    <h1>Success!</h1>
    <h2>Your shortened URL:</h2>
    <p><a href="{shortened_url}">{shortened_url}</a></p>
  </div>
  <div class="row">
    <div class="col-sm-12">
      <h3>The original link URL</h3>
      <p><a href="{original_url}">{original_url}</a></p>
    </div>
  </div>
</div>

</body>
</html>
`

const SUBMIT_PAGE = `
<!DOCTYPE html>
<html lang="en">
<head>
  <title>Shorten a Link</title>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="//maxcdn.bootstrapcdn.com/bootstrap/3.3.6/css/bootstrap.min.css">
  <script src="//ajax.googleapis.com/ajax/libs/jquery/1.12.0/jquery.min.js"></script>
  <script src="//maxcdn.bootstrapcdn.com/bootstrap/3.3.6/js/bootstrap.min.js"></script>
</head>
<body>

<div class="container-fluid" style="text-align:center;">
  <div class="jumbotron">
    <h1>Shorten A URL</h1>
    <h2>Please enter the URL to shorten:</h2>
    <form action="newLink" method="POST">
    <div class="form-inline" role="form">
        <div class="form-group">
            <label class="sr-only" for="long_url">URL:</label>
            <input type="url" name="long_url" class="form-control" style="min-width: 300px; margin-right: 20px">
        </div>

        <button type="submit" class="btn btn-primary">Shorten it!</button>
    </form>
  </div>
</div>

</body>
</html>
`

const DOMAIN = "localhost:8888"

func generateSuccessPage(original_url string, shortened_url string) string {
	// any value less than zero results in infinite replacements
	page := SUCCESS_PAGE
	page = strings.Replace(page, "{shortened_url}", html.EscapeString(shortened_url), -1)
	page = strings.Replace(page, "{original_url}", html.EscapeString(original_url), -1)
	return page
}

const database_filename = "shortener.sqlite3"
const PORTNO = ":8888"

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

func getShortUrlFromId(id int64, https bool) string {
	/* Get a shortened URL from an id */
	var protocol string
	if https {
		// connection is secure
		protocol = "https://"
	} else {
		protocol = "http://"
	}
	return protocol + DOMAIN + "/" + stringFromId(id)
}

func main() {
	db, err := sql.Open("sqlite3", database_filename)
	if err != nil {
		log.Fatal(err)
	}
	// Ping actually writes the file to disk if it doesn't exist yet
	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sqlStmt := "CREATE TABLE IF NOT EXISTS Urls ( id integer NOT NULL PRIMARY KEY, url text UNIQUE);"
	_, err = db.Exec(sqlStmt)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/newLink", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			// User trying to GET the page
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		req.ParseForm()
		var rawUrl = req.Form.Get("long_url")
		if strings.Compare(rawUrl, "") == 0 {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		parsedUrl, err := url.Parse(rawUrl)
		if err != nil {
			// Malformed input from the user
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		longUrl := parsedUrl.String()
		// db.Exec sanitizes our input for us if we use the question marks
		res, err := db.Exec("insert into urls(id, url) values(?, ?)", nil, longUrl)

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

		fmt.Printf("%v: %v --> %v/%v\n", id, longUrl, PORTNO, stringFromId(id))
		var urlString string
		if req.TLS == nil {
			urlString = "http://"
		} else {
			urlString = "https://"
		}
		urlString += DOMAIN + "/success"
		redirUrl, _ := url.Parse(urlString)
		q := redirUrl.Query()
		q.Set("short", stringFromId(id))
		redirUrl.RawQuery = q.Encode()
		fmt.Println(redirUrl.String())
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
		err = db.QueryRow("select url from urls where id = ?", id).Scan(&url)
		if err != nil {
			http.NotFound(w, req)
			return
		}
		fmt.Fprintf(w, generateSuccessPage(url, getShortUrlFromId(id, req.TLS != nil)))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/" {
			fmt.Fprintf(w, SUBMIT_PAGE)
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
		err = db.QueryRow("select url from urls where id = ?", id).Scan(&url)
		if err != nil {
			http.NotFound(w, req)
			return
		}

		//fmt.Fprintf(w, generateSuccessPage(url, getShortUrlFromId(id, req.TLS != nil)))
		http.Redirect(w, req, url, http.StatusMovedPermanently)
	})

	log.Fatal(http.ListenAndServe(PORTNO, nil))
}
