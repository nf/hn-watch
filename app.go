/*
Copyright 2013 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"text/template"
	"unicode"

	"appengine"
	"appengine/datastore"
	"appengine/delay"
	"appengine/mail"
	"appengine/urlfetch"

	"github.com/PuerkitoBio/goquery"
)

const (
	pollURL  = hnURL
	hnURL    = "https://news.ycombinator.com/"
	mailFrom = "adg@google.com"
	mailTo   = "adg@google.com"
)

var keywords = []string{
	"go",
	"golang",
	"google",
}

type Link struct {
	Title   string
	URL     string
	ItemURL string
}

func init() {
	http.HandleFunc("/poll", poll)
}

func poll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	client := urlfetch.Client(c)

	res, err := client.Get(pollURL)
	if err != nil {
		report(c, w, err, "Error fetching page")
		return
	}
	if res.StatusCode != http.StatusOK {
		report(c, w, errors.New(res.Status), "Error fetching page")
		return
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	res.Body.Close()
	if err != nil {
		report(c, w, err, "Error parsing page")
		return
	}

	doc.Find("td.title > a").Each(func(_ int, s *goquery.Selection) {
		if title := s.Text(); matchTitle(title) {
			href, _ := s.Attr("href")
			l := &Link{
				Title:   title,
				URL:     href,
				ItemURL: itemURL(s),
			}
			if err := notify(c, l); err != nil {
				report(c, w, err, "Error sending notification")
				return
			}
		}
	})

	w.Write([]byte("OK"))
}

func report(c appengine.Context, w http.ResponseWriter, err error, desc string) {
	c.Errorf("%v: %v", desc, err)
	http.Error(w, desc, http.StatusInternalServerError)
}

func matchTitle(s string) bool {
	for _, w := range strings.Fields(s) {
		w = strings.TrimFunc(w, notLetter)
		w = strings.ToLower(w)
		for _, kw := range keywords {
			if w == kw {
				return true
			}
		}
	}
	return false
}

func notLetter(r rune) bool {
	return !unicode.IsLetter(r)
}

func itemURL(s *goquery.Selection) (url string) {
	s.Closest("tr").Next().Find("a").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if strings.HasPrefix(href, "item?id=") {
			url = hnURL + href
		}
	})
	return
}

func notify(c appengine.Context, l *Link) error {
	k := datastore.NewKey(c, "Link", l.ItemURL, 0, nil)
	// Put the Link in the datastore and send an email notification,
	// but only if we haven't seen this item before.
	err := datastore.RunInTransaction(c, func(c appengine.Context) error {
		err := datastore.Get(c, k, &Link{})
		if err == nil || err != datastore.ErrNoSuchEntity {
			return err
		}
		if _, err := datastore.Put(c, k, l); err != nil {
			return err
		}
		notifyLater.Call(c, l)
		return nil
	}, nil)
	return err
}

var notifyLater = delay.Func("notify", notifyFunc)

func notifyFunc(c appengine.Context, l *Link) {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, l); err != nil {
		c.Errorf("rendering email template: %v", err)
		return
	}
	if err := mail.Send(c, &mail.Message{
		Sender:  mailFrom,
		To:      []string{mailTo},
		Subject: "HN: " + l.Title,
		Body:    body.String(),
	}); err != nil {
		c.Errorf("sending email: %v", err)
	}
}

var tmpl = template.Must(template.New("email").Parse(`
A new item has appeared on Hacker News.

Title: {{.Title}}
URL: {{.URL}}
Discussion: {{.ItemURL}}
`))
