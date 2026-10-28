// Package provider — Terraform-провайдер Gemini (Plugin Framework).
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/itlabsio/terraform-provider-gemini/internal/client"
)

var _ provider.Provider = (*geminiProvider)(nil)

type geminiProvider struct {
	version string
}

// New возвращает фабрику провайдера для providerserver.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &geminiProvider{version: version} }
}

type providerModel struct {
	URL           types.String `tfsdk:"url"`
	Token         types.String `tfsdk:"token"`
	OIDCIssuerURL types.String `tfsdk:"oidc_issuer_url"`
	ClientID      types.String `tfsdk:"client_id"`
	ClientSecret  types.String `tfsdk:"client_secret"`
}

func (p *geminiProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "gemini"
	resp.Version = p.version
}

func (p *geminiProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Управление инстансом Gemini через его API. Нужна client-роль superuser: " +
			"создавать проекты и инстансы может только она.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional:    true,
				Description: "Базовый URL инстанса Gemini (backend, без /api). Env: GEMINI_URL.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Готовый access-токен Keycloak. Env: GEMINI_TOKEN. Взаимоисключим с client credentials.",
			},
			"oidc_issuer_url": schema.StringAttribute{
				Optional:    true,
				Description: "Issuer (realm Keycloak), тот же, что OIDC_ISSUER_URL backend'а. Env: GEMINI_OIDC_ISSUER_URL.",
			},
			"client_id": schema.StringAttribute{
				Optional:    true,
				Description: "Клиент Keycloak с service account (client credentials). Env: GEMINI_CLIENT_ID.",
			},
			"client_secret": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Секрет клиента service account. Env: GEMINI_CLIENT_SECRET.",
			},
		},
	}
}

// pick — значение из конфигурации, иначе из переменной окружения.
func pick(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *geminiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := client.New(client.Config{
		URL:          pick(m.URL, "GEMINI_URL"),
		Token:        pick(m.Token, "GEMINI_TOKEN"),
		IssuerURL:    pick(m.OIDCIssuerURL, "GEMINI_OIDC_ISSUER_URL"),
		ClientID:     pick(m.ClientID, "GEMINI_CLIENT_ID"),
		ClientSecret: pick(m.ClientSecret, "GEMINI_CLIENT_SECRET"),
		UserAgent:    "terraform-provider-gemini/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *geminiProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newProjectResource,
		newSourceInstanceResource,
	}
}

func (p *geminiProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

// clientFrom достаёт клиента из ProviderData (nil — провайдер ещё не сконфигурирован).
func clientFrom(data any, diags interface {
	AddError(string, string)
}) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", "expected *client.Client")
		return nil
	}
	return c
}
