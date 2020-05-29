package cloudfoundry

import (
	"fmt"

	"code.cloudfoundry.org/cli/actor/sharedaction"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3"
	ccWrapper "code.cloudfoundry.org/cli/api/cloudcontroller/wrapper"
	"code.cloudfoundry.org/cli/api/router"
	routerWrapper "code.cloudfoundry.org/cli/api/router/wrapper"
	"code.cloudfoundry.org/cli/api/uaa"
	"code.cloudfoundry.org/cli/api/uaa/constant"
	uaaWrapper "code.cloudfoundry.org/cli/api/uaa/wrapper"
	"code.cloudfoundry.org/cli/command/translatableerror"
	"code.cloudfoundry.org/cli/util/configv3"
)

// Config -
type Config struct {
	Endpoint          string
	Username          string
	Password          string
	Token             string
	SSOPasscode       string
	CFClientID        string
	CFClientSecret    string
	UaaClientID       string
	UaaClientSecret   string
	SkipSslValidation bool
	OrgName           string
	SpaceName         string
}

// Session - wraps the available clients from CF cli
type Session struct {
	ClientV3      *ccv3.Client
	ConfigV3      *configv3.Config
	ClientV2      *ccv2.Client
	ClientUAA     *uaa.Client
	ClientRouting *router.Client
	CLIClient     *CLIClient
	ApiEndpoint   string
}

// NewSession -
func NewSession(c Config) (s *Session, err error) {
	if c.Username == "" && c.CFClientID == "" && c.Token == "" {
		return nil, fmt.Errorf("user/password or uaa_client_id/uaa_client_secret or token must be set")
	}
	if c.Username != "" && c.CFClientID == "" {
		c.CFClientID = "cf"
		c.CFClientSecret = ""
	}
	if c.Password == "" && c.CFClientID != "cf" && c.CFClientSecret != "" {
		c.Username = ""
	}
	s = &Session{
		ApiEndpoint: c.Endpoint,
		CLIClient: &CLIClient{
			Username:    c.Username,
			Password:    c.Password,
			Endpoint:    c.Endpoint,
			TargetOrg:   c.OrgName,
			TargetSpace: c.SpaceName,
		},
	}
	config := &configv3.Config{
		ConfigFile: configv3.JSONConfig{
			ConfigVersion:        3,
			Target:               c.Endpoint,
			UAAOAuthClient:       c.CFClientID,
			UAAOAuthClientSecret: c.CFClientSecret,
			AccessToken:          c.Token,
			SkipSSLValidation:    c.SkipSslValidation,
		},
		ENV: configv3.EnvOverride{
			CFUsername: c.Username,
			CFPassword: c.Password,
			BinaryName: "terraform-provider",
		},
	}
	uaaClientId := c.UaaClientID
	uaaClientSecret := c.UaaClientSecret
	if uaaClientId == "" {
		uaaClientId = c.CFClientID
		uaaClientSecret = c.CFClientSecret
	}
	configUaa := &configv3.Config{
		ConfigFile: configv3.JSONConfig{
			ConfigVersion:        3,
			UAAOAuthClient:       uaaClientId,
			UAAOAuthClientSecret: uaaClientSecret,
			SkipSSLValidation:    c.SkipSslValidation,
		},
	}

	err = s.init(config, configUaa, c)
	if err != nil {
		return nil, fmt.Errorf("Error when creating clients: %s", err.Error())
	}

	return s, nil
}

func (s *Session) init(config *configv3.Config, configUaa *configv3.Config, configSess Config) error {
	// -------------------------
	// Create v3 and v2 clients
	ccWrappersV2 := []ccv2.ConnectionWrapper{}
	ccWrappersV3 := []ccv3.ConnectionWrapper{}
	authWrapperV2 := ccWrapper.NewUAAAuthentication(nil, config)
	authWrapperV3 := ccWrapper.NewUAAAuthentication(nil, config)

	ccWrappersV2 = append(ccWrappersV2, authWrapperV2)
	ccWrappersV2 = append(ccWrappersV2, ccWrapper.NewRetryRequest(config.RequestRetryCount()))

	ccWrappersV3 = append(ccWrappersV3, authWrapperV3)
	ccWrappersV3 = append(ccWrappersV3, ccWrapper.NewRetryRequest(config.RequestRetryCount()))
	ccClientV2 := ccv2.NewClient(ccv2.Config{
		AppName:            config.BinaryName(),
		AppVersion:         config.BinaryVersion(),
		JobPollingTimeout:  config.OverallPollingTimeout(),
		JobPollingInterval: config.PollingInterval(),
		Wrappers:           ccWrappersV2,
	})

	ccClientV3 := ccv3.NewClient(ccv3.Config{
		AppName:            config.BinaryName(),
		AppVersion:         config.BinaryVersion(),
		JobPollingTimeout:  config.OverallPollingTimeout(),
		JobPollingInterval: config.PollingInterval(),
		Wrappers:           ccWrappersV3,
	})

	_, err := ccClientV2.TargetCF(ccv2.TargetSettings{
		URL:               config.Target(),
		SkipSSLValidation: config.SkipSSLValidation(),
		DialTimeout:       config.DialTimeout(),
	})
	if err != nil {
		return fmt.Errorf("Error creating ccv2 client: %s", err)
	}
	if ccClientV2.AuthorizationEndpoint() == "" {
		return translatableerror.AuthorizationEndpointNotFoundError{}
	}

	_, _, err = ccClientV3.TargetCF(ccv3.TargetSettings{
		URL:               config.Target(),
		SkipSSLValidation: config.SkipSSLValidation(),
		DialTimeout:       config.DialTimeout(),
	})
	if err != nil {
		return fmt.Errorf("Error creating ccv3 client: %s", err)
	}

	// -------------------------
	// create an uaa client with cf_username/cf_password or client_id/client secret
	// to use it in v2 and v3 api for authenticate requests
	uaaClient := uaa.NewClient(config)

	uaaAuthWrapper := uaaWrapper.NewUAAAuthentication(nil, configUaa)
	uaaClient.WrapConnection(uaaAuthWrapper)
	uaaClient.WrapConnection(uaaWrapper.NewRetryRequest(config.RequestRetryCount()))
	err = uaaClient.SetupResources(ccClientV2.AuthorizationEndpoint())
	if err != nil {
		return fmt.Errorf("Error setup resource uaa: %s", err)
	}

	// -------------------------
	// Obtain access and refresh tokens
	var accessToken string
	var refreshToken string

	if configSess.Token != "" {
		accessToken = configSess.Token
	} else if configSess.SSOPasscode != "" {
		// try connecting with SSO passcode to retrieve access token and refresh token
		accessToken, refreshToken, err = uaaClient.Authenticate(map[string]string{
			"passcode": configSess.SSOPasscode,
		}, "", constant.GrantTypePassword)
		if err != nil {
			return fmt.Errorf("Error when authenticate on cf using SSO passcode: %s", err)
		}
	} else if config.CFUsername() != "" {
		// try connecting with pair given on uaa to retrieve access token and refresh token
		accessToken, refreshToken, err = uaaClient.Authenticate(map[string]string{
			"username": config.CFUsername(),
			"password": config.CFPassword(),
		}, "", constant.GrantTypePassword)
		if err != nil {
			return fmt.Errorf("Error when authenticate on cf using user/pass: %s", err)
		}
	} else if config.UAAOAuthClient() != "cf" {
		accessToken, refreshToken, err = uaaClient.Authenticate(map[string]string{
			"client_id":     config.UAAOAuthClient(),
			"client_secret": config.UAAOAuthClientSecret(),
		}, "", constant.GrantTypeClientCredentials)
		if err != nil {
			return fmt.Errorf("Error when authenticate on cf using client_id/secret: %s", err)
		}
	}
	if accessToken == "" {
		return fmt.Errorf("A pair of username/password, a pair of client_id/client_secret, or a SSO passcode must be set.")
	}

	config.SetAccessToken(fmt.Sprintf("bearer %s", accessToken))
	if refreshToken != "" {
		config.SetRefreshToken(refreshToken)
	}

	// -------------------------
	// assign uaa client to request wrappers
	uaaAuthWrapper.SetClient(uaaClient)
	authWrapperV2.SetClient(uaaClient)
	authWrapperV3.SetClient(uaaClient)
	// -------------------------
	// -------------------------
	// Create router client for tcp routing
	routerConfig := router.Config{
		AppName:    config.BinaryName(),
		AppVersion: config.BinaryVersion(),
		ConnectionConfig: router.ConnectionConfig{
			DialTimeout:       config.DialTimeout(),
			SkipSSLValidation: config.SkipSSLValidation(),
		},
		RoutingEndpoint: ccClientV2.RoutingEndpoint(),
	}

	routerWrappers := []router.ConnectionWrapper{}

	rAuthWrapper := routerWrapper.NewUAAAuthentication(uaaClient, config)
	errorWrapper := routerWrapper.NewErrorWrapper()
	retryWrapper := newRetryRequestRouter(config.RequestRetryCount())

	routerWrappers = append(routerWrappers, rAuthWrapper, retryWrapper, errorWrapper)
	routerConfig.Wrappers = routerWrappers

	s.ClientRouting = router.NewClient(routerConfig)
	// -------------------------

	// store client in the sessions
	s.ClientV3 = ccClientV3
	s.ClientV2 = ccClientV2
	s.ConfigV3 = config
	s.ClientUAA = uaaClient
	// -------------------------

	return nil
}

func (s *Session) BinaryName() string {
	return "cf-observability"
}

func (s *Session) CurrentUserName() (string, error) {
	return s.ConfigV3.CurrentUserName()
}

func (s *Session) HasTargetedOrganization() bool {
	return s.ConfigV3.HasTargetedOrganization()
}

func (s *Session) HasTargetedSpace() bool {
	return s.ConfigV3.HasTargetedSpace()
}

func (s *Session) TargetedSpace() configv3.Space {
	return s.ConfigV3.TargetedSpace()
}

func (s *Session) TargetedOrganization() configv3.Organization {
	return s.ConfigV3.TargetedOrganization()
}

func (s *Session) RefreshToken() string {
	return s.ConfigV3.RefreshToken()
}

func (s *Session) AccessToken() string {
	return s.ConfigV3.AccessToken()
}

func (s *Session) TargetedOrganizationName() string {
	return s.ConfigV3.TargetedOrganizationName()
}

func (s *Session) Verbose() (bool, []string) {
	return s.ConfigV3.Verbose()
}

// should implement sharedaction.Config
var _ sharedaction.Config = &Session{}
