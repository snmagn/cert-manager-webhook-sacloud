package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"

	"github.com/jetstack/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/jetstack/cert-manager/pkg/acme/webhook/cmd"
	cmmeta "github.com/jetstack/cert-manager/pkg/apis/meta/v1"

	"github.com/sacloud/libsacloud/v2/sacloud"
	"github.com/sacloud/libsacloud/v2/sacloud/search"
)

// GroupName api-resource group
var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	// This will register our custom DNS provider with the webhook serving
	// library, making it available as an API under the provided GroupName.
	// You can register multiple DNS provider implementations with a single
	// webhook, where the Name() method will be used to disambiguate between
	// the different implementations.
	cmd.RunWebhookServer(GroupName,
		&customDNSProviderSolver{},
	)
}

// customDNSProviderSolver implements the provider-specific logic needed to
// 'present' an ACME challenge TXT record for your own DNS provider.
// To do so, it must implement the `github.com/jetstack/cert-manager/pkg/acme/webhook.Solver`
// interface.
type customDNSProviderSolver struct {
	// If a Kubernetes 'clientset' is needed, you must:
	// 1. uncomment the additional `client` field in this structure below
	// 2. uncomment the "k8s.io/client-go/kubernetes" import at the top of the file
	// 3. uncomment the relevant code in the Initialize method below
	// 4. ensure your webhook's service account has the required RBAC role
	//    assigned to it for interacting with the Kubernetes APIs you need.
	client *kubernetes.Clientset
}

// customDNSProviderConfig is a structure that is used to decode into when
// solving a DNS01 challenge.
// This information is provided by cert-manager, and may be a reference to
// additional configuration that's needed to solve the challenge for this
// particular certificate or issuer.
// This typically includes references to Secret resources containing DNS
// provider credentials, in cases where a 'multi-tenant' DNS solver is being
// created.
// If you do *not* require per-issuer or per-certificate configuration to be
// provided to your webhook, you can skip decoding altogether in favour of
// using CLI flags or similar to provide configuration.
// You should not include sensitive information here. If credentials need to
// be used by your provider here, you should reference a Kubernetes Secret
// resource and fetch these credentials using a Kubernetes clientset.
type customDNSProviderConfig struct {
	// Change the two fields below according to the format of the configuration
	// to be decoded.
	// These fields will be set by users in the
	// `issuer.spec.acme.dns01.providers.webhook.config` field.

	APIAccessTokenRef  cmmeta.SecretKeySelector `json:"apiAccessTokenRef"`
	APIAccessSecretRef cmmeta.SecretKeySelector `json:"apiAccessSecretRef"`
	APIZoneRef         cmmeta.SecretKeySelector `json:"apiZoneRef"`
}

// Name is used as the name for this DNS solver when referencing it on the ACME
// Issuer resource.
// This should be unique **within the group name**, i.e. you can have two
// solvers configured with the same Name() **so long as they do not co-exist
// within a single webhook deployment**.
// For example, `cloudflare` may be used as the name of a solver.
func (c *customDNSProviderSolver) Name() string {
	return "sacloud"
}

// Present is responsible for actually presenting the DNS record with the
// DNS provider.
// This method should tolerate being called multiple times with the same value.
// cert-manager itself will later perform a self check to ensure that the
// solver has correctly configured the DNS provider.
func (c *customDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	klog.V(6).Infof("call function Present: namespace=%s, zone=%s, fqdn=%s",
		ch.ResourceNamespace, ch.ResolvedZone, ch.ResolvedFQDN)

	entry, domain := c.getDomainAndEntry(ch)
	klog.V(6).Infof("present for entry=%s, domain=%s", entry, domain)

	// create dns resource manager
	dnsOp, apiZone, err := c.getSacloudDNSOpAndZone(ch)
	if err != nil {
		return err
	}

	// execute search
	searched, err := c.findTargetDNS(dnsOp, apiZone, ch, domain)
	if err != nil {
		return fmt.Errorf("unable to sacloud api: %v", err)
	}

	if searched.Total == 1 {
		var dns = searched.DNS[0]

		var targetTxtRecord *sacloud.DNSRecord
		for _, record := range dns.Records {
			if record.Name == entry && record.Type == "TXT" {
				if record.RData == ch.Key {
					// 既に同様のキーでDNS01チャレンジ用のTXTレコードが作成されている場合は処理をスキップする
					return nil
				}
				targetTxtRecord = record
			}
		}

		if targetTxtRecord != nil {
			// TXTレコード更新
			klog.V(6).Infof("Update TXT Record")
			targetTxtRecord.RData = ch.Key
		} else {
			// TXTレコード追加
			klog.V(6).Infof("Append TXT Record")
			dns.Records = append(dns.Records, &sacloud.DNSRecord{
				Type:  "TXT",
				Name:  entry,
				RData: ch.Key,
				TTL:   60,
			})
		}

		// 更新リクエスト
		dns, err = dnsOp.UpdateSettings(context.Background(), dns.ID, &sacloud.DNSUpdateSettingsRequest{
			Records: dns.Records,
		})
		if err != nil {
			return fmt.Errorf("update failed!unable to sacloud api: %v", err)
		}
	} else {
		return fmt.Errorf("uninitialized zone: %s, error: %v", domain, nil)
	}

	return nil
}

// CleanUp should delete the relevant TXT record from the DNS provider console.
// If multiple TXT records exist with the same record name (e.g.
// _acme-challenge.example.com) then **only** the record with the same `key`
// value provided on the ChallengeRequest should be cleaned up.
// This is in order to facilitate multiple DNS validations for the same domain
// concurrently.
func (c *customDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	entry, domain := c.getDomainAndEntry(ch)
	klog.V(6).Infof("present for entry=%s, domain=%s", entry, domain)

	// create dns resource manager
	dnsOp, apiZone, err := c.getSacloudDNSOpAndZone(ch)
	if err != nil {
		return err
	}

	// execute search
	searched, err := c.findTargetDNS(dnsOp, apiZone, ch, domain)
	if err != nil {
		return fmt.Errorf("unable to sacloud api: %v", err)
	}

	if searched.Total == 1 {
		var dns = searched.DNS[0]

		var records []*sacloud.DNSRecord
		for _, record := range dns.Records {
			if record.Type != "TXT" || record.RData != ch.Key {
				records = append(records, record)
			}
		}

		// 更新リクエスト
		dns, err = dnsOp.UpdateSettings(context.Background(), dns.ID, &sacloud.DNSUpdateSettingsRequest{
			Records: records,
		})
		if err != nil {
			return fmt.Errorf("cleanup failed!unable to sacloud api: %v", err)
		}
	} else {
		return fmt.Errorf("uninitialized zone: %s, error: %v", domain, nil)
	}

	return nil
}

// Initialize will be called when the webhook first starts.
// This method can be used to instantiate the webhook, i.e. initialising
// connections or warming up caches.
// Typically, the kubeClientConfig parameter is used to build a Kubernetes
// client that can be used to fetch resources from the Kubernetes API, e.g.
// Secret resources containing credentials used to authenticate with DNS
// provider accounts.
// The stopCh can be used to handle early termination of the webhook, in cases
// where a SIGTERM or similar signal is sent to the webhook process.
func (c *customDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	klog.V(6).Infof("call function Initialize")
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}
	c.client = cl
	return nil
}

// loadConfig is a small helper function that decodes JSON configuration into
// the typed config struct.
func loadConfig(cfgJSON *extapi.JSON) (customDNSProviderConfig, error) {
	cfg := customDNSProviderConfig{}
	// handle the 'base case' where no configuration has been provided
	if cfgJSON == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	return cfg, nil
}

func (c *customDNSProviderSolver) getDomainAndEntry(ch *v1alpha1.ChallengeRequest) (string, string) {
	// Both ch.ResolvedZone and ch.ResolvedFQDN end with a dot: '.'
	entry := strings.TrimSuffix(ch.ResolvedFQDN, ch.ResolvedZone)
	entry = strings.TrimSuffix(entry, ".")
	domain := strings.TrimSuffix(ch.ResolvedZone, ".")
	return entry, domain
}

func (c *customDNSProviderSolver) getSacloudDNSOpAndZone(ch *v1alpha1.ChallengeRequest) (*sacloud.DNSOp, *string, error) {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load config: %v", err)
	}
	klog.V(9).Infof("Decoded configuration %v", cfg)
	apiToken, apiSecret, apiZone, err := c.getAccountInfo(&cfg, ch.ResourceNamespace)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get API Account Info: %v", err)
	}

	// create client
	caller := &sacloud.Client{
		AccessToken:       *apiToken,
		AccessTokenSecret: *apiSecret,
		UserAgent:         "sacloud/cert-manager",
		RetryMax:          sacloud.APIDefaultRetryMax,
		RetryWaitMin:      sacloud.APIDefaultRetryWaitMin,
		RetryWaitMax:      sacloud.APIDefaultRetryWaitMax,
	}

	// create dns resource manager
	dnsOp := sacloud.NewDNSOp(caller).(*sacloud.DNSOp)

	return dnsOp, apiZone, nil
}

func (c *customDNSProviderSolver) findTargetDNS(dnsOp *sacloud.DNSOp, apiZone *string, ch *v1alpha1.ChallengeRequest, domain string) (*sacloud.DNSFindResult, error) {
	// create search condition
	condition := &sacloud.FindCondition{
		Filter: search.Filter{
			search.Key("Name"):      search.AndEqual(domain),
			search.Key("Zone.Name"): search.AndEqual(*apiZone),
		},
	}

	// execute search
	return dnsOp.Find(context.Background(), condition)
}

func (c *customDNSProviderSolver) getAccountInfo(cfg *customDNSProviderConfig, namespace string) (*string, *string, *string, error) {
	// apiToken
	secretName := cfg.APIAccessTokenRef.LocalObjectReference.Name
	klog.V(6).Infof("try to load secret `%s` with key `%s`", secretName, cfg.APIAccessTokenRef.Key)
	sec, err := c.client.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to get secret `%s`; %v", secretName, err)
	}
	secBytes, ok := sec.Data[cfg.APIAccessTokenRef.Key]
	if !ok {
		return nil, nil, nil, fmt.Errorf("key %q not found in secret \"%s/%s\"", cfg.APIAccessTokenRef.Key,
			cfg.APIAccessTokenRef.LocalObjectReference.Name, namespace)
	}
	apiToken := string(secBytes)

	// apiAccessSecret
	secretName = cfg.APIAccessSecretRef.LocalObjectReference.Name
	klog.V(6).Infof("try to load secret `%s` with key `%s`", secretName, cfg.APIAccessSecretRef.Key)
	sec, err = c.client.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to get secret `%s`; %v", secretName, err)
	}
	secBytes, ok = sec.Data[cfg.APIAccessSecretRef.Key]
	if !ok {
		return nil, nil, nil, fmt.Errorf("key %q not found in secret \"%s/%s\"", cfg.APIAccessSecretRef.Key,
			cfg.APIAccessSecretRef.LocalObjectReference.Name, namespace)
	}
	apiSecret := string(secBytes)

	// apiZone
	secretName = cfg.APIZoneRef.LocalObjectReference.Name
	klog.V(6).Infof("try to load secret `%s` with key `%s`", secretName, cfg.APIZoneRef.Key)
	sec, err = c.client.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to get secret `%s`; %v", secretName, err)
	}
	secBytes, ok = sec.Data[cfg.APIZoneRef.Key]
	if !ok {
		return nil, nil, nil, fmt.Errorf("key %q not found in secret \"%s/%s\"", cfg.APIZoneRef.Key,
			cfg.APIZoneRef.LocalObjectReference.Name, namespace)
	}
	apiZone := string(secBytes)

	return &apiToken, &apiSecret, &apiZone, nil
}
