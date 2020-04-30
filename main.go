package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/drone/drone-go/drone"
	"github.com/drone/drone-go/plugin/config"
	"github.com/google/go-github/github"
	"github.com/kelseyhightower/envconfig"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Secret                  string `envconfig:"DRONE_YAML_SECRET"`
	Addr                    string `envconfig:"DRONE_PLUGIN_ADDR"`
	GithubAPPID             string `envconfig:"DRONE_PLUGIN_GITHUB_APP_ID"`
	GithubAPPInstallationID string `envconfig:"DRONE_PLUGIN_GITHUB_APP_INSTALLATION_ID"`
	GithubAPPPrivateKey     string `envconfig:"DRONE_PLUGIN_GITHUB_APP_PRIVATE_KEY"`
}

type plugin struct {
	client *github.Client
}

func (p *plugin) Find(ctx context.Context, req *config.Request) (*drone.Config, error) {
	entry, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, req.Repo.Config, &github.RepositoryContentGetOptions{Ref: req.Build.After})
	if err != nil {
		return nil, err
	}
	entryBody, err := entry.GetContent()
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(entryBody))
	var records []map[string]interface{}
	var dependsOn []string
	for {
		record := map[string]interface{}{}
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if record["kind"] == "virtual-pipeline" {
			pipelines, ok := record["pipelines"].([]string)
			if !ok {
				continue
			}

			for _, k := range pipelines {
				dependsOn = append(dependsOn, k)
				// TODO parallelism of fetching drone.yml
				content, _, _, err := p.client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, filepath.Join(k, req.Repo.Config), &github.RepositoryContentGetOptions{Ref: req.Build.After})
				if err != nil {
					return nil, err
				}

				body, err := content.GetContent()
				if err != nil {
					return nil, err
				}
				record := map[string]interface{}{}
				if err := yaml.Unmarshal([]byte(body), &record); err != nil {
					return nil, err
				}

				records = append(records, record)
			}
			continue
		}
		records = append(records, record)
	}

	output := bytes.NewBuffer(nil)
	encoder := yaml.NewEncoder(output)
	for _, record := range records {
		if record["injectDependencies"] == true {
			record["depends_on"] = dependsOn
		}
		if err := encoder.Encode(&record); err != nil {
			return nil, err
		}
	}

	return &drone.Config{
		Data: output.String(),
	}, nil
}

type githubAppAuthenticator struct {
	id                 string
	installationID     string
	privateKey         []byte
	accessToken        string
	accessTokenExpires time.Time
	accessTokenOnce    *sync.Once
	accessTokenError   error
}

func (a *githubAppAuthenticator) RoundTrip(req *http.Request) (*http.Response, error) {
	if a.accessTokenExpires.Before(time.Now().Add(time.Second * 10)) {
		a.accessTokenOnce.Do(a.getAccessToken)
		if a.accessTokenError != nil {
			return nil, a.accessTokenError
		}
	}

	req.Header.Set("Authorization", "token "+a.accessToken)
	return http.DefaultClient.Do(req)
}

func (a *githubAppAuthenticator) getAccessToken() {
	a.accessTokenError = nil
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &jwt.StandardClaims{Issuer: a.id, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Second * 10).Unix()})
	tokString, err := tok.SignedString(a.privateKey)
	if err != nil {
		a.accessTokenError = err
		return
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", a.installationID), nil)
	if err != nil {
		a.accessTokenError = err
		return
	}

	req.Header.Set("Authorization", "Bearer "+tokString)
	req.Header.Set("Accept", "application/vnd.github.machine-man-preview+json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.accessTokenError = err
		return
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		a.accessTokenError = err
		return
	}

	if res.StatusCode%100 != 2 {
		a.accessTokenError = errors.New(string(body))
		return
	}

	result := map[string]interface{}{}
	if err := json.Unmarshal(body, &result); err != nil {
		a.accessTokenError = err
		return
	}

	accessToken, ok := result["token"].(string)
	if !ok {
		a.accessTokenError = errors.New(string(body))
		return
	}

	expire, ok := result["expires_at"].(string)
	if !ok {
		a.accessTokenError = errors.New(string(body))
		return
	}
	expireAt, err := time.Parse("2006-01-02T15:04:05Z", expire)
	if err != nil {
		a.accessTokenError = err
		return
	}
	a.accessToken = accessToken
	a.accessTokenExpires = expireAt
}

func main() {
	cfg := &Config{}
	envconfig.MustProcess("", cfg)
	client := &http.Client{
		Transport: &githubAppAuthenticator{
			id:              cfg.GithubAPPID,
			installationID:  cfg.GithubAPPInstallationID,
			privateKey:      []byte(cfg.GithubAPPPrivateKey),
			accessTokenOnce: &sync.Once{},
		},
	}

	p := &plugin{
		client: github.NewClient(client),
	}
	handler := config.Handler(p, cfg.Secret, logrus.StandardLogger())
	http.Handle("/", handler)
	logrus.Fatal(http.ListenAndServe(cfg.Addr, nil))
}
