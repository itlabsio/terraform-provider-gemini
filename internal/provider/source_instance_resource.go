package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/itlabsio/terraform-provider-gemini/internal/client"
)

var (
	_ resource.Resource                   = (*sourceInstanceResource)(nil)
	_ resource.ResourceWithConfigure      = (*sourceInstanceResource)(nil)
	_ resource.ResourceWithImportState    = (*sourceInstanceResource)(nil)
	_ resource.ResourceWithValidateConfig = (*sourceInstanceResource)(nil)
)

// Ограничения повторяют валидацию backend'а (api/instances.go), чтобы ошибка
// всплывала на plan, а не на apply.
var (
	instanceNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9])?$`)
	uuidRe         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sslModes       = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
)

type sourceInstanceResource struct {
	client *client.Client
}

func newSourceInstanceResource() resource.Resource { return &sourceInstanceResource{} }

type sourceInstanceModel struct {
	ID                     types.String `tfsdk:"id"`
	Name                   types.String `tfsdk:"name"`
	Project                types.String `tfsdk:"project"`
	Host                   types.String `tfsdk:"host"`
	Port                   types.Int64  `tfsdk:"port"`
	SSLMode                types.String `tfsdk:"ssl_mode"`
	SSLRootCert            types.String `tfsdk:"ssl_root_cert"`
	DiscoveryDB            types.String `tfsdk:"discovery_db"`
	ExcludedDatabases      types.List   `tfsdk:"excluded_databases"`
	AuthType               types.String `tfsdk:"auth_type"`
	PlainUsername          types.String `tfsdk:"plain_username"`
	PlainPasswordWO        types.String `tfsdk:"plain_password_wo"`
	PlainPasswordWOVersion types.Int64  `tfsdk:"plain_password_wo_version"`
	VaultPath              types.String `tfsdk:"vault_path"`
	VaultRole              types.String `tfsdk:"vault_role"`
	VaultUsernameKey       types.String `tfsdk:"vault_username_key"`
	VaultPasswordKey       types.String `tfsdk:"vault_password_key"`
	ServerVersionNum       types.Int64  `tfsdk:"server_version_num"`
	CreatedAt              types.String `tfsdk:"created_at"`
}

func (r *sourceInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_source_instance"
}

// optString — необязательная строка с дефолтом "": значение из API всегда
// известно, и null/"" не дают ложного diff'а.
func optString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		Optional:    true,
		Computed:    true,
		Default:     stringdefault.StaticString(""),
		Description: desc,
		Validators:  validators,
	}
}

func (r *sourceInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Source-инстанс PostgreSQL на source-инстансе Gemini (с него снимаются дампы). " +
			"Базы появляются после discovery в UI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "UUID инстанса в Gemini.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Уникальное имя; задаёт S3-префикс дампов (<name>/<база>). " +
					"Переименования нет — смена имени пересоздаёт инстанс.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(instanceNameRe,
					"1-63 chars of [a-zA-Z0-9_-], starting and ending with a letter or digit")},
			},
			"project": optString("Проект-владелец (gemini_project.x.name). Пусто — инстанс без проекта, доступен только superuser."),
			"host": schema.StringAttribute{
				Required:    true,
				Description: "Хост PostgreSQL (или pgbouncer/пулера), доступный из кластера Gemini.",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"port": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(5432),
				Description: "Порт PostgreSQL. По умолчанию 5432.",
				Validators:  []validator.Int64{int64validator.Between(1, 65535)},
			},
			"ssl_mode": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("require"),
				Description: "Режим libpq: " + strings.Join(sslModes, ", ") + ".",
				Validators:  []validator.String{stringvalidator.OneOf(sslModes...)},
			},
			"ssl_root_cert": optString("PEM кастомного корневого CA. Только при ssl_mode verify-ca / verify-full."),
			"discovery_db":  optString("База для discovery. Пусто — postgres."),
			"excluded_databases": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
				Description: "Базы, которые discovery игнорирует (postgres пропускается всегда).",
				Validators: []validator.List{
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`),
						"must be non-empty and without spaces")),
				},
			},
			"auth_type": schema.StringAttribute{
				Required:    true,
				Description: "plain — логин/пароль хранятся в Gemini; vault — креды читаются из Vault.",
				Validators:  []validator.String{stringvalidator.OneOf("plain", "vault")},
			},
			"plain_username": optString("Логин (auth_type = plain)."),
			"plain_password_wo": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				Description: "Пароль (auth_type = plain). Write-only: не попадает в state, API его не возвращает. " +
					"Отправляется при создании и при смене plain_password_wo_version.",
			},
			"plain_password_wo_version": schema.Int64Attribute{
				Optional:    true,
				Description: "Увеличьте, чтобы отправить новый plain_password_wo.",
			},
			"vault_path":         optString("Путь секрета в Vault (auth_type = vault)."),
			"vault_role":         optString("Роль Vault."),
			"vault_username_key": optString("Поле секрета с логином. Задаётся вместе с vault_password_key; пусто — автоопределение."),
			"vault_password_key": optString("Поле секрета с паролем."),
			"server_version_num": schema.Int64Attribute{
				Computed:      true,
				Description:   "server_version_num по последнему discovery (0 — не выполнялся).",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Время создания инстанса в Gemini (RFC 3339, UTC).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *sourceInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func known(v interface {
	IsNull() bool
	IsUnknown() bool
}) bool {
	return !v.IsNull() && !v.IsUnknown()
}

func (r *sourceInstanceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c sourceInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(c.AuthType) {
		switch c.AuthType.ValueString() {
		case "plain":
			if !c.PlainUsername.IsUnknown() && c.PlainUsername.ValueString() == "" {
				resp.Diagnostics.AddAttributeError(path.Root("plain_username"),
					"plain_username is required", "auth_type = \"plain\" requires plain_username.")
			}
		case "vault":
			if !c.VaultPath.IsUnknown() && c.VaultPath.ValueString() == "" {
				resp.Diagnostics.AddAttributeError(path.Root("vault_path"),
					"vault_path is required", "auth_type = \"vault\" requires vault_path.")
			}
		}
	}
	if !c.VaultUsernameKey.IsUnknown() && !c.VaultPasswordKey.IsUnknown() &&
		(c.VaultUsernameKey.ValueString() == "") != (c.VaultPasswordKey.ValueString() == "") {
		resp.Diagnostics.AddAttributeError(path.Root("vault_password_key"),
			"vault keys must be set together",
			"Set both vault_username_key and vault_password_key, or neither for auto-detection.")
	}
	// Backend молча выбрасывает CA при не-verify режиме — это дало бы вечный diff.
	if known(c.SSLRootCert) && c.SSLRootCert.ValueString() != "" && !c.SSLMode.IsUnknown() {
		mode := c.SSLMode.ValueString()
		if mode != "verify-ca" && mode != "verify-full" {
			resp.Diagnostics.AddAttributeError(path.Root("ssl_root_cert"),
				"ssl_root_cert requires a verifying ssl_mode",
				"ssl_root_cert is applied only with ssl_mode = \"verify-ca\" or \"verify-full\".")
		}
	}
}

// input собирает тело запроса из плана. Пароль берётся из конфигурации
// (write-only в плане всегда null) и передаётся, только если sendPassword.
func (m *sourceInstanceModel) input(ctx context.Context, password string, sendPassword bool) (client.InstanceInput, error) {
	excluded := []string{}
	if known(m.ExcludedDatabases) {
		if d := m.ExcludedDatabases.ElementsAs(ctx, &excluded, false); d.HasError() {
			return client.InstanceInput{}, errorFromDiags(d)
		}
	}
	project := m.Project.ValueString()
	in := client.InstanceInput{
		Role:              "source",
		Project:           &project,
		Name:              m.Name.ValueString(),
		Host:              m.Host.ValueString(),
		Port:              int(m.Port.ValueInt64()),
		SSLMode:           m.SSLMode.ValueString(),
		SSLRootCert:       m.SSLRootCert.ValueString(),
		DiscoveryDB:       m.DiscoveryDB.ValueString(),
		ExcludedDatabases: excluded,
		AuthType:          m.AuthType.ValueString(),
		PlainUsername:     m.PlainUsername.ValueString(),
		VaultPath:         m.VaultPath.ValueString(),
		VaultRole:         m.VaultRole.ValueString(),
		VaultUsernameKey:  m.VaultUsernameKey.ValueString(),
		VaultPasswordKey:  m.VaultPasswordKey.ValueString(),
	}
	if sendPassword {
		in.PlainPassword = password
	}
	return in, nil
}

// fill переносит ответ API в модель. Write-only и его версия не трогаются.
func (m *sourceInstanceModel) fill(inst *client.Instance) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Project = types.StringValue(inst.Project)
	m.Host = types.StringValue(inst.Host)
	m.Port = types.Int64Value(int64(inst.Port))
	m.SSLMode = types.StringValue(inst.SSLMode)
	m.SSLRootCert = types.StringValue(inst.SSLRootCert)
	m.DiscoveryDB = types.StringValue(inst.DiscoveryDB)
	vals := make([]attr.Value, 0, len(inst.ExcludedDatabases))
	for _, d := range inst.ExcludedDatabases {
		vals = append(vals, types.StringValue(d))
	}
	m.ExcludedDatabases = types.ListValueMust(types.StringType, vals)
	m.AuthType = types.StringValue(inst.AuthType)
	m.PlainUsername = types.StringValue(inst.PlainUsername)
	m.VaultPath = types.StringValue(inst.VaultPath)
	m.VaultRole = types.StringValue(inst.VaultRole)
	m.VaultUsernameKey = types.StringValue(inst.VaultUsernameKey)
	m.VaultPasswordKey = types.StringValue(inst.VaultPasswordKey)
	m.ServerVersionNum = types.Int64Value(int64(inst.ServerVersionNum))
	m.CreatedAt = types.StringValue(inst.CreatedAt.UTC().Format(time.RFC3339))
	m.PlainPasswordWO = types.StringNull()
}

func errorFromDiags(d diag.Diagnostics) error {
	for _, e := range d.Errors() {
		return errors.New(e.Summary() + ": " + e.Detail())
	}
	return nil
}

func (r *sourceInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg sourceInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := plan.input(ctx, cfg.PlainPasswordWO.ValueString(), plan.AuthType.ValueString() == "plain")
	if err != nil {
		resp.Diagnostics.AddError("Invalid configuration", err.Error())
		return
	}
	inst, err := r.client.CreateInstance(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create instance", err.Error())
		return
	}
	plan.fill(inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sourceInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sourceInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, err := r.client.GetInstance(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance", err.Error())
		return
	}
	if inst.Role != "source" {
		resp.Diagnostics.AddError("Not a source instance",
			"instance "+inst.Name+" has role "+inst.Role+"; gemini_source_instance manages only source instances")
		return
	}
	state.fill(inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sourceInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg sourceInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Пустой пароль в PUT backend трактует как «оставить сохранённый», поэтому
	// пароль шлём только при смене версии или переходе vault → plain.
	sendPassword := plan.AuthType.ValueString() == "plain" &&
		(!plan.PlainPasswordWOVersion.Equal(state.PlainPasswordWOVersion) ||
			state.AuthType.ValueString() != "plain")
	in, err := plan.input(ctx, cfg.PlainPasswordWO.ValueString(), sendPassword)
	if err != nil {
		resp.Diagnostics.AddError("Invalid configuration", err.Error())
		return
	}
	inst, err := r.client.UpdateInstance(ctx, state.ID.ValueString(), in)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update instance", err.Error())
		return
	}
	plan.fill(inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sourceInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sourceInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteInstance(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete instance", err.Error())
	}
}

// ImportState принимает UUID инстанса или его имя (имя уникально).
func (r *sourceInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !uuidRe.MatchString(id) {
		items, err := r.client.ListInstances(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Failed to list instances", err.Error())
			return
		}
		id = ""
		for _, it := range items {
			if it.Name == req.ID {
				id = it.ID
				break
			}
		}
		if id == "" {
			resp.Diagnostics.AddError("Instance not found", "no instance named "+req.ID)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
