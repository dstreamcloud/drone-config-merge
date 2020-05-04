package plugin

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/tidwall/gjson"
)

type Authenticator struct {
	id             string
	installationID string
	privateKey     *rsa.PrivateKey
	token          string
	expires        time.Time
	err            error
	loading        uint32
	mu             *sync.RWMutex
}

func NewAuthenticator(id string, installationID string, privKey *rsa.PrivateKey) *Authenticator {
	return &Authenticator{
		id:             id,
		installationID: installationID,
		privateKey:     privKey,
		mu:             &sync.RWMutex{},
	}
}

func (a *Authenticator) RoundTrip(req *http.Request) (*http.Response, error) {
	if a.token == "" || a.expires.Before(time.Now().Add(time.Second*10)) {
		if atomic.LoadUint32(&a.loading) == 0 {
			a.getAccessToken()
		} else {
			a.mu.RLock()
			a.mu.RUnlock()
		}
		if a.err != nil {
			return nil, a.err
		}
	}

	req.Header.Set("Authorization", "token "+a.token)
	return http.DefaultClient.Do(req)
}

func (a *Authenticator) getAccessToken() {
	a.mu.Lock()
	defer a.mu.Unlock()
	atomic.StoreUint32(&a.loading, 1)
	defer atomic.StoreUint32(&a.loading, 0)
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	defer cancel()
	a.err = nil
	a.token = ""
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &jwt.StandardClaims{Issuer: a.id, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Second * 10).Unix()})
	tokString, err := tok.SignedString(a.privateKey)
	if err != nil {
		a.err = err
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", a.installationID), nil)
	if err != nil {
		a.err = err
		return
	}

	req.Header.Set("Authorization", "Bearer "+tokString)
	req.Header.Set("Accept", "application/vnd.github.machine-man-preview+json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.err = err
		return
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		a.err = err
		return
	}

	if res.StatusCode/100 != 2 {
		a.err = errors.New(string(body))
		return
	}

	a.token = gjson.GetBytes(body, "token").String()
	a.expires = gjson.GetBytes(body, "expires_at").Time()
}
