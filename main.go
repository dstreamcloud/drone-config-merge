package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"

	"github.com/drone/drone-go/plugin/config"
	"github.com/dstreamcloud/drone-config-merge/plugin"
	"github.com/google/go-github/github"
	"github.com/kelseyhightower/envconfig"
	"github.com/sirupsen/logrus"
)

type Config struct {
	Secret                  string `envconfig:"DRONE_YAML_SECRET"`
	Addr                    string `envconfig:"DRONE_PLUGIN_ADDR"`
	GithubAPPID             string `envconfig:"DRONE_PLUGIN_GITHUB_APP_ID"`
	GithubAPPInstallationID string `envconfig:"DRONE_PLUGIN_GITHUB_APP_INSTALLATION_ID"`
	GithubAPPPrivateKey     string `envconfig:"DRONE_PLUGIN_GITHUB_APP_PRIVATE_KEY"`
}

func main() {
	cfg := &Config{}
	envconfig.MustProcess("", cfg)
	privPem, _ := pem.Decode([]byte(cfg.GithubAPPPrivateKey))
	privKey, err := x509.ParsePKCS1PrivateKey(privPem.Bytes)
	if err != nil {
		panic(err)
	}

	client := &http.Client{
		Transport: plugin.NewAuthenticator(cfg.GithubAPPID, cfg.GithubAPPInstallationID, privKey),
	}

	p := plugin.New(github.NewClient(client))
	var handler http.Handler
	if os.Getenv("IS_DEVELOPMENT") == "1" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := &config.Request{}
			if err := json.NewDecoder(r.Body).Decode(req); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			res, err := p.Find(r.Context(), req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(res)
		})
	} else {
		handler = config.Handler(p, cfg.Secret, logrus.StandardLogger())
	}
	http.Handle("/", handler)
	logrus.Fatal(http.ListenAndServe(cfg.Addr, nil))
}
