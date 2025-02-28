package caddyapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type Route struct {
	Match    []Match  `json:"match"`
	Handle   []Handle `json:"handle"`
	Terminal bool     `json:"terminal"`
}

type Match struct {
	Host []string `json:"host"`
}

type Handle struct {
	Handler   string     `json:"handler"`
	Routes    []Route    `json:"routes,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
}

type Upstream struct {
	Dial string `json:"dial"`
}

type TLSPolicy struct {
	Match                TLSMatch                `json:"match"`
	CertificateSelection TLSCertificateSelection `json:"certificate_selection"`
}

type TLSMatch struct {
	SNI []string `json:"sni"`
}

type TLSCertificateSelection struct {
	AnyTag []string `json:"any_tag"`
}

type TLSFileLoad struct {
	Certificate string   `json:"certificate"`
	Key         string   `json:"key"`
	Tags        []string `json:"tags"`
}

// TODO: we should think about how to handle the case when use would like to deploy GitOps on server where Caddy is already running
func AddCaddyRecords(gitopsName, domain string, certs, noIde bool) error {
	caddyAPIRoutesBaseUrl := "http://localhost:2019/config/apps/http/servers/srv0/routes/..."
	caddyAPITLSBaseUrl := "http://localhost:2019/config/apps/tls/certificates/load_files/..."
	caddyAPITLSPoliciesBaseUrl := "http://localhost:2019/config/apps/http/servers/srv0/tls_connection_policies/..."

	routes := []Route{}

	// GitOps route
	routes = append(routes, Route{
		Match: []Match{{Host: []string{fmt.Sprintf("%s.%s", gitopsName, domain)}}},
		Handle: []Handle{{Handler: "subroute", Routes: []Route{
			{
				Handle: []Handle{{Handler: "reverse_proxy", Upstreams: []Upstream{
					{Dial: fmt.Sprintf("%s:8079", gitopsName)},
				}}},
			},
		}}},
		Terminal: true,
	})

	// Bitswan editor route
	if !noIde{
		routes = append(routes, Route{
			Match: []Match{
				{
					Host: []string{fmt.Sprintf("editor.%s", domain)},
				},
			},
			Handle: []Handle{
				{
					Handler: "subroute",
					Routes: []Route{
						{
							Handle: []Handle{
								{
									Handler: "reverse_proxy",
									Upstreams: []Upstream{
										{
											Dial: fmt.Sprintf("bitswan-editor-%s:9999", gitopsName),
										},
									},
								},
							},
						},
					},
				},
			},
			Terminal: true,
		})
	}

	if certs {
		tlsPolicy := []TLSPolicy{
			{
				Match: TLSMatch{
					SNI: []string{
						fmt.Sprintf("*.%s", domain),
					},
				},
				CertificateSelection: TLSCertificateSelection{
					AnyTag: []string{gitopsName},
				},
			},
		}

		tlsLoad := []TLSFileLoad{
			{
				Certificate: fmt.Sprintf("/tls/%s/full-chain.pem", domain),
				Key:         fmt.Sprintf("/tls/%s/private-key.pem", domain),
				Tags:        []string{gitopsName},
			},
		}

		// send tls policy and tls to caddy api
		jsonPayload, err := json.Marshal(tlsLoad)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON payload: %w", err)
		}

		// Send the payload to the Caddy API
		err = sendRequest("POST", caddyAPITLSBaseUrl, jsonPayload)
		if err != nil {
			return fmt.Errorf("Failed to add TLS to Caddy: %w", err)
		}

		jsonPayload, err = json.Marshal(tlsPolicy)
		if err != nil {
			return fmt.Errorf("Failed to marshal TLS Policy JSON: %w", err)
		}

		// Send the payload to the Caddy API
		err = sendRequest("POST", caddyAPITLSPoliciesBaseUrl, jsonPayload)
		if err != nil {
			return fmt.Errorf("Failed to add TLS policies to Caddy: %w", err)
		}
	}

	jsonPayload, err := json.Marshal(routes)
	if err != nil {
		return fmt.Errorf("Failed to marshal routes payload: %w", err)
	}

	// Send the payload to the Caddy API
	err = sendRequest("POST", caddyAPIRoutesBaseUrl, jsonPayload)
	if err != nil {
		return fmt.Errorf("Failed to add routes to Caddy: %w", err)
	}

	return nil
}

func InitCaddy() error {
	urls := []string{
		"http://localhost:2019/config/apps/http/servers/srv0/routes",
		"http://localhost:2019/config/apps/http/servers/srv0/listen",
		"http://localhost:2019/config/apps/tls/certificates/load_files",
		"http://localhost:2019/config/apps/http/servers/srv0/tls_connection_policies",
	}

	for idx, url := range urls {
		var payload []byte
		if idx == 1 {
			payload = []byte(`[":80", ":443"]`)
		} else {
			payload = []byte(`[]`)
		}

		if err := sendRequest("PUT", url, payload); err != nil {
			return fmt.Errorf("failed to initialize Caddy: %w", err)
		}
	}

	fmt.Println("Caddy initialized successfully!")
	return nil
}

func sendRequest(method, url string, payload []byte) error {
	client := &http.Client{}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Failed to call Caddy API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Caddy API returned status code %d", resp.StatusCode)
	}

	return nil
}
