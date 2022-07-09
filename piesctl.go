// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"fmt"
	"net"
	"net/url"
	"net/http"
	"context"
	"errors"
	"io/ioutil"
	"encoding/json"
	"regexp"
	"syscall"
)

// ----------------------------------
// Pies CTL API
// ----------------------------------

type PiesResponse struct {
	Status string
	Message string
	//Parser_messages []string
}

var allIPRx = regexp.MustCompile(`^(0\.0\.0\.0)?(:.+)`)

func PiesClient(controlURL *url.URL, method, path string, retval interface{}) (reterr error) {
	clt := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				switch controlURL.Scheme {
				case `local`, `file`, `unix`:
					return net.Dial(`unix`, controlURL.Path)
				case `inet`:
					host := controlURL.Host
					if m := allIPRx.FindStringSubmatch(host); m != nil {
						host = `127.0.0.1` + m[2]
					}
					return net.Dial(`tcp`, host)
				}
				return nil, errors.New("Scheme not implemented")
			},
		},
	}

	rurl := &url.URL{Scheme: "http", Path: path}
	if controlURL.Scheme == "inet" {
		rurl.Host = controlURL.Host
	} else {
		rurl.Host = "localhost"
	}

	req, err := http.NewRequest(method, rurl.String(), nil)
	if err != nil {
		reterr  = err
		return
	}
	resp, err := clt.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			reterr = fmt.Errorf("can't connect to pies: not running?")
		} else {
			reterr = fmt.Errorf("can't query: %v", err)
		}
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		reterr = fmt.Errorf("can't read response: %v", err)
		return
	}

	if retval != nil {
		reterr = json.Unmarshal(body, retval)
	}
	return
}

func PiesStopInstance(controlURL *url.URL) error {
	var resp PiesResponse
	if err := PiesClient(controlURL, http.MethodDelete, `/instance/PID`, &resp); err != nil {
		return err
	}
	if resp.Status != "OK" {
		return errors.New(resp.Message)
	}

	return nil
}

func PiesRestartInstance(controlURL *url.URL) error {
	var resp PiesResponse
	if err := PiesClient(controlURL, http.MethodPut, `/instance/PID`, &resp); err != nil {
		return err
	}
	if resp.Status != "OK" {
		return errors.New(resp.Message)
	}

	return nil
}

func PiesReloadConfig(controlURL *url.URL) error {
	var rresp PiesResponse
	err := PiesClient(controlURL, http.MethodPut, `/conf/runtime`, &rresp)
	if err != nil {
		return err
	}

	if rresp.Status != "OK" {
		return errors.New(rresp.Message)
	}
	return nil
}

type PiesInstanceInfo struct {
	PID int             `json:"PID"`
	Args []string       `json:"argv"`
	Binary string       `json:"binary"`
	InstanceName string `json:"instance"`
	PackageName string  `json:"package"`
	Version string      `json:"version"`
}

func GetPiesInstanceInfo(controlURL *url.URL) (err error, info PiesInstanceInfo) {
	err = PiesClient(controlURL, http.MethodGet, `/instance`, &info)
	return
}

type PiesComponentInfo struct {
	Mode string         `json:"mode"`
	Status string       `json:"status"`
	PID int             `json:"PID"`
	URL string          `json:"URL"`
	Service string      `json:"service"`
	TcpMUXMaster string `json:"master"`
	Runlevels string    `json:"runlevels"`
	WakeupTime int      `json:"wakeup-time"`
	Args []string       `json:"argv"`
	Command string      `json:"command"`
}

func GetPiesComponentInfo(controlURL *url.URL) (err error, info []PiesComponentInfo) {
	err = PiesClient(controlURL, http.MethodGet, `/programs`, &info)
	return
}

