// Package client — минимальный HTTP-клиент к /api/* инстанса Gemini.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// Config — параметры подключения. Либо Token (готовый access-токен), либо
// IssuerURL + ClientID + ClientSecret (client credentials service account'а Keycloak).
type Config struct {
	URL          string
	Token        string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	UserAgent    string
}

// Client ходит в API от имени service account'а.
type Client struct {
	base      string
	http      *http.Client
	userAgent string
}

// New собирает клиента; токен по client credentials получается и обновляется лениво.
func New(cfg Config) (*Client, error) {
	base := strings.TrimSuffix(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return nil, errors.New("url is required")
	}
	var src oauth2.TokenSource
	switch {
	case cfg.Token != "":
		src = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	case cfg.IssuerURL != "" && cfg.ClientID != "" && cfg.ClientSecret != "":
		cc := clientcredentials.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			// Как и backend (auth.NewVerifier), считаем issuer realm'ом Keycloak.
			TokenURL: strings.TrimSuffix(cfg.IssuerURL, "/") + "/protocol/openid-connect/token",
		}
		// Контекст живёт дольше Configure: им же делаются обновления токена.
		src = cc.TokenSource(context.Background())
	default:
		return nil, errors.New("either token or oidc_issuer_url + client_id + client_secret must be set")
	}
	hc := oauth2.NewClient(context.Background(), src)
	hc.Timeout = 60 * time.Second
	return &Client{base: base, http: hc, userAgent: cfg.UserAgent}, nil
}

// APIError — ответ API с кодом не 2xx.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("gemini API: %d: %s", e.Status, e.Message) }

// IsNotFound — ресурса нет (404).
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// По нему backend помечает созданное/изменённое как managed_by = terraform
	// (бейдж в UI). Старый backend заголовок просто игнорирует.
	req.Header.Set("X-Gemini-Managed-By", "terraform")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Project — проект RBAC (`/api/projects`).
type Project struct {
	Name          string    `json:"name"`
	InstanceCount int       `json:"instance_count"`
	CreatedBy     string    `json:"created_by"`
	ManagedBy     string    `json:"managed_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// GetProject ищет проект по имени (отдельного GET у API нет — читаем список).
func (c *Client) GetProject(ctx context.Context, name string) (*Project, error) {
	var items []Project
	if err := c.do(ctx, http.MethodGet, "/api/projects", nil, &items); err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: "project " + name + " not found"}
}

func (c *Client) CreateProject(ctx context.Context, name string) (*Project, error) {
	var p Project
	err := c.do(ctx, http.MethodPost, "/api/projects", map[string]string{"name": name}, &p)
	return &p, err
}

func (c *Client) DeleteProject(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/projects/"+url.PathEscape(name), nil, nil)
}

// Instance — инстанс PostgreSQL в ответе API (пароль API не отдаёт никогда).
type Instance struct {
	ID                string    `json:"id"`
	Role              string    `json:"role"`
	Project           string    `json:"project"`
	Name              string    `json:"name"`
	Host              string    `json:"host"`
	Port              int       `json:"port"`
	AuthType          string    `json:"auth_type"`
	PlainUsername     string    `json:"plain_username"`
	VaultPath         string    `json:"vault_path"`
	VaultRole         string    `json:"vault_role"`
	VaultUsernameKey  string    `json:"vault_username_key"`
	VaultPasswordKey  string    `json:"vault_password_key"`
	SSLMode           string    `json:"ssl_mode"`
	SSLRootCert       string    `json:"ssl_root_cert"`
	DiscoveryDB       string    `json:"discovery_db"`
	ExcludedDatabases []string  `json:"excluded_databases"`
	ServerVersionNum  int       `json:"server_version_num"`
	CreatedAt         time.Time `json:"created_at"`
}

// InstanceInput — тело POST/PUT /api/instances. Пустой PlainPassword при
// обновлении означает «оставить сохранённый».
type InstanceInput struct {
	Role              string   `json:"role"`
	Project           *string  `json:"project,omitempty"`
	Name              string   `json:"name"`
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	SSLMode           string   `json:"ssl_mode"`
	SSLRootCert       string   `json:"ssl_root_cert"`
	DiscoveryDB       string   `json:"discovery_db"`
	ExcludedDatabases []string `json:"excluded_databases"`
	AuthType          string   `json:"auth_type"`
	PlainUsername     string   `json:"plain_username"`
	PlainPassword     string   `json:"plain_password,omitempty"`
	VaultPath         string   `json:"vault_path"`
	VaultRole         string   `json:"vault_role"`
	VaultUsernameKey  string   `json:"vault_username_key"`
	VaultPasswordKey  string   `json:"vault_password_key"`
}

func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	var items []Instance
	err := c.do(ctx, http.MethodGet, "/api/instances", nil, &items)
	return items, err
}

func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var inst Instance
	err := c.do(ctx, http.MethodGet, "/api/instances/"+url.PathEscape(id), nil, &inst)
	return &inst, err
}

func (c *Client) CreateInstance(ctx context.Context, in InstanceInput) (*Instance, error) {
	var inst Instance
	err := c.do(ctx, http.MethodPost, "/api/instances", in, &inst)
	return &inst, err
}

func (c *Client) UpdateInstance(ctx context.Context, id string, in InstanceInput) (*Instance, error) {
	var inst Instance
	err := c.do(ctx, http.MethodPut, "/api/instances/"+url.PathEscape(id), in, &inst)
	return &inst, err
}

func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/instances/"+url.PathEscape(id), nil, nil)
}
