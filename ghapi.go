// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"os"
	"time"
	"fmt"
	"errors"
	"net/http"
	"github.com/graygnuorg/go-gdbm"
	"encoding/json"
	"path/filepath"
	"strings"
	"io"
	"io/ioutil"
	"runtime"
)

// ----------------------------------
// Token database
// ----------------------------------

const (
	EntityEnterprise = iota
	EntityOrg
	EntityRepo

	RemoveToken = `remove-token`
	RegistrationToken = `registration-token`
)

var GHEntityPrefix = []string{
	`/enterprises/`,
	`/orgs/`,
	`/repos/`,
}

func (ent entityValue) BaseKey() string {
	return GHEntityPrefix[ent.Type] + ent.Name
}

func (ent entityValue) PATKey() string {
	if n := strings.IndexRune(ent.Name, '/'); n != -1 {
		return GHEntityPrefix[ent.Type] + ent.Name[:n]
	}
	return ent.BaseKey()
}

func (ent entityValue) TokenKey(kind, project string) string {
	//FIXME
	if ent.Type == EntityRepo {
		return ent.BaseKey() + `/actions/runners/` + kind
	} else {
		return ent.BaseKey() + `/` + project + `/actions/runners/` + kind
	}
}

func (ent entityValue) ProjectURL(name string) string {
	//FIXME
	return `https://github.com/` + ent.Name + `/` + name
}

type GHToken struct {
	Token string         `json:"token"`
	ExpiresAt time.Time  `json:"expires_at"`
}

var (
	ErrTokenNotFound = errors.New("Token not found")
)

func SaveToken(key string, token GHToken) error {
	js, err := json.Marshal(token)
	if err != nil {
		return nil
	}

	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	if err := db.Store([]byte(key), js, true); err != nil {
		return fmt.Errorf("can't store key %s: %v", key, err)
	}
	return nil
}

func FetchRawToken(key string) (GHToken, error) {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeReader)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = ErrTokenNotFound
		}
		return GHToken{}, err
	}
	defer db.Close()
	if js, err := db.Fetch([]byte(key)); err == nil {
		var tok GHToken
		err := json.Unmarshal(js, &tok)
		return tok, err
	} else if errors.Is(err, gdbm.ErrItemNotFound) {
		return GHToken{}, ErrTokenNotFound
	} else {
		return GHToken{}, err
	}
}

func FetchToken(key string) (string, error) {
	if tok, err := FetchRawToken(key); err == nil {
		if time.Now().Before(tok.ExpiresAt) {
			return tok.Token, nil
		}
		return "", ErrTokenNotFound
	} else {
		return "", err
	}
}

func DeleteToken(key string) error {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	return db.Delete([]byte(key))
}

func PrefixIterator(pfx string) (func () (string, GHToken, error), error) {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeReader)
	if err != nil {
		return nil, err
	}

	next := db.Iterator()
	return func() (key string, tok GHToken, err error) {
		for {
			var b []byte
			b, err = next()
			if err == nil {
				key = string(b)
				if key != pfx && strings.TrimPrefix(key, pfx) != key {
					var js []byte
					if js, err = db.Fetch(b); err == nil {
						if err = json.Unmarshal(js, &tok); err == nil {
							return
						}
					}
					break
				}
			} else {
				break
			}
		}
		db.Close()
		return
	}, nil
}


func getGitHubToken(key, pat string) (token GHToken, err error) {
	var req *http.Request
	req, err = http.NewRequest(http.MethodPost, `https://api.github.com` + key, nil)
	if err != nil {
		return
	}

	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "token " + pat)
	fmt.Printf("Getting token for %s\n", req.URL.String())
	//fmt.Printf("%#v",req)
	var resp *http.Response
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return
	}
	if resp.StatusCode == 201 {
		err = json.Unmarshal(body, &token)
	} else {
		err = ErrTokenNotFound
	}
	return
}

func GetBaseKey(key string) (patkey string, ispat bool) {
	for _, pfx := range GHEntityPrefix {
		if s := strings.TrimPrefix(key, pfx); s != key {
			if n := strings.IndexRune(s, '/'); n == -1 {
				patkey = key
				ispat = true
			} else {
				patkey = pfx
				ispat = false
				patkey = patkey + s[:n]
			}
			break
		}
	}
	return
}

func GetToken(key string) (string, error) {
	if token, err := FetchToken(key); err == nil {
		return token, err
	} else if errors.Is(err, ErrTokenNotFound) {
		if patkey, ispat := GetBaseKey(key); ispat {
			return "", err
		} else if token, err := FetchToken(patkey); err == nil {
			if tok, err := getGitHubToken(key, token); err != nil {
				return "", err
			} else {
				SaveToken(key, tok)
				return tok.Token, err
			}
		} else {
			return "", err
		}
	} else {
		return "", err
	}
}

type GHDownload struct {
	OS string        `json:"os"`
	Arch string      `json:"architecture"`
	URL string       `json:"download_url"`
	FileName string  `json:"filename"`
	CheckSum string  `json:"sha256_checksum"`
}

func GitHubGetDownloads(ent entityValue) (downloads []GHDownload, err error) {
	var req *http.Request

	key := ent.BaseKey() + `/actions/runners/downloads`

	req, err = http.NewRequest(http.MethodGet, `https://api.github.com` + key, nil)
	if err != nil {
		return
	}

	var pat string
	if pat, err = FetchToken(ent.PATKey()); err != nil {
		return
	}

	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "token " + pat)
	var resp *http.Response
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return
	}
	if resp.StatusCode == 200 {
		err = json.Unmarshal(body, &downloads)
	} else {
		err = errors.New(resp.Status)
	}
	return
}

func GitHubSelectDownload(ent entityValue) (GHDownload, error) {
	res, err := GitHubGetDownloads(ent)
	if err == nil {
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x64"
		}
		fmt.Printf("Looking for runner tarball for %s %s\n", runtime.GOOS, arch)
		for _, dn := range res {
			if dn.OS == runtime.GOOS && dn.Arch == arch {
				return dn, nil
			}
		}
	}
	return GHDownload{}, err
}

func DownloadFile(name, url string) error {
	out, err := os.Create(name)
	if err != nil  {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil  {
		os.Remove(name)
	}
	return err
}

func GetRunnerArchive(ent entityValue) (filename string, err error) {
	var dn GHDownload
	if dn, err = GitHubSelectDownload(ent); err != nil {
		return
	}
	filename = filepath.Join(config.CacheDir, dn.FileName)
	_, err = os.Stat(filename)
	switch {
	case err == nil:
		fmt.Printf("Using cached copy %s\n", filename)
		break
	case os.IsNotExist(err):
		fmt.Printf("Downloading %s\n", dn.URL)
		err = DownloadFile(filename, dn.URL)
	default:
		err = fmt.Errorf("Can't stat %s: %v", filename, err)
	}
	return
}
