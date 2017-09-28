// Copyright 2015-2017 Applatix, Inc. All rights reserved.
package configuration

import (
	"fmt"
	"regexp"
	"time"

	"applatix.io/axdb"
	"applatix.io/axerror"
	"applatix.io/axops/utils"
	"applatix.io/restcl"
)

type ConfigurationData struct {
	ConfigurationUser        string            `json:"user"`
	ConfigurationName        string            `json:"name"`
	ConfigurationDesc        string            `json:"description,omitempty"`
	ConfigurationIsSecrets   bool              `json:"is_secret"`
	ConfigurationValue       map[string]string `json:"value,omitempty"`
	ConfigurationDateCreated int64             `json:"ctime,omitempty"`
	ConfigurationLastUpdated int64             `json:"mtime,omitempty"`
}

type ConfigurationContext struct {
	User string
	Name string
	Key  string
}

type SecretResult struct {
	SecretData     map[string]string `json:"data"`
	SecretMetadata map[string]string `json:"metadata"`
}

var (
	ConfigurationStrRegex = regexp.MustCompile("^%%config\\.([^ ]*)\\.([-0-9A-Za-z_]+)\\.([-0-9A-Za-z_]+)%%$")
)

var MaxRetryDuration time.Duration = 60 * time.Second

var retryConfig *restcl.RetryConfig = &restcl.RetryConfig{
	Timeout: MaxRetryDuration,
}

func GetConfigurations(params map[string]interface{}) ([]ConfigurationData, *axerror.AXError) {
	configs := []ConfigurationData{}
	axErr := utils.Dbcl.Get(axdb.AXDBAppAXOPS, ConfigurationTableName, params, &configs)
	if axErr != nil {
		return nil, axErr
	}
	return configs, nil
}

func GetConfigurationsByUser(user string) ([]ConfigurationData, *axerror.AXError) {
	configs, axErr := GetConfigurations(map[string]interface{}{
		ConfigurationUserName: user,
	})
	if axErr != nil {
		return nil, axErr
	}
	return configs, nil
}

func GetConfigurationsByUserName(user string, name string) ([]ConfigurationData, *axerror.AXError) {
	configs, axErr := GetConfigurations(map[string]interface{}{
		ConfigurationName:     name,
		ConfigurationUserName: user,
	})
	if axErr != nil {
		return nil, axErr
	}
	return configs, nil
}

func GetConfiguration(user string, name string) (*ConfigurationData, *axerror.AXError) {
	configs, axErr := GetConfigurationsByUserName(user, name)
	if axErr != nil {
		return nil, axErr
	}
	if len(configs) == 0 {
		return nil, axerror.ERR_API_RESOURCE_NOT_FOUND.NewWithMessagef("Configuration does not exist with user %s, name %s", user, name)
	}
	if len(configs) != 1 {
		return nil, axerror.ERR_API_INTERNAL_ERROR.NewWithMessagef("More than one configuration exist with user %s, name %s", user, name)
	}
	return &configs[0], nil
}

// redactSecretValues is a helper to return a new map where all config values are empty strings
// This is used to ensure we do not store any config secret in axdb, but can still indicate the available keys in the API/UI
func redactSecretValues(strMap map[string]string) map[string]string {
	emptyValues := make(map[string]string)
	for key := range strMap {
		emptyValues[key] = ""
	}
	return emptyValues
}

func CreateConfiguration(config *ConfigurationData) *axerror.AXError {
	//Check whether this is configured as Kubernetes secrets
	if config.ConfigurationIsSecrets {
		axErr := CreateKubernetesSecret(config)
		if axErr != nil {
			return axErr
		}
		config.ConfigurationValue = redactSecretValues(config.ConfigurationValue)
	}
	// Update timestamp
	config.ConfigurationDateCreated = time.Now().Unix()
	config.ConfigurationLastUpdated = config.ConfigurationDateCreated
	_, axErr := utils.Dbcl.Post(axdb.AXDBAppAXOPS, ConfigurationTableName, config)
	if axErr != nil {
		return axErr
	}
	return nil
}

func UpdateConfiguration(config *ConfigurationData) *axerror.AXError {
	//Check whether this is configured as Kubernetes secrets
	if config.ConfigurationIsSecrets {
		axErr := CreateKubernetesSecret(config)
		if axErr != nil {
			return axErr
		}
		config.ConfigurationValue = redactSecretValues(config.ConfigurationValue)
	}
	config.ConfigurationLastUpdated = time.Now().Unix()
	_, axErr := utils.Dbcl.Put(axdb.AXDBAppAXOPS, ConfigurationTableName, config)
	if axErr != nil {
		return axErr
	}
	return nil
}

func DeleteConfiguration(config *ConfigurationData) *axerror.AXError {
	//Check whether this is configured as Kubernetes secrets
	if config.ConfigurationIsSecrets {
		axErr := DeleteKubernetesSecret(config)
		if axErr != nil {
			return axErr
		}
	}
	_, axErr := utils.Dbcl.Delete(axdb.AXDBAppAXOPS, ConfigurationTableName, []*ConfigurationData{config})
	if axErr != nil {
		return axErr
	}
	return nil
}

// ConfigStringToContext converts a config string (e.g. %%config.joe@example.com.sql.username%%) to a ConfigurationContext instance
func ConfigStringToContext(configStr string) (*ConfigurationContext, *axerror.AXError) {
	matched := ConfigurationStrRegex.FindStringSubmatch(configStr)
	if len(matched) != 4 {
		return nil, axerror.ERR_API_INTERNAL_ERROR.NewWithMessagef("%s is an invalid configuration variable expression", configStr)
	}
	configCtx := ConfigurationContext{
		User: matched[1],
		Name: matched[2],
		Key:  matched[3],
	}
	return &configCtx, nil
}

// ProcessConfigurationStr takes a configuration string (e.g. %%config.mynamespace.password%%), and returns the value
// If the configuration is a secret, returns nil
func ProcessConfigurationStr(configStr string) (*string, *axerror.AXError) {
	configCtx, axErr := ConfigStringToContext(configStr)
	if axErr != nil {
		return nil, axErr
	}
	config, axErr := GetConfiguration(configCtx.User, configCtx.Name)
	if axErr != nil {
		return nil, axErr
	}
	configVal, ok := config.ConfigurationValue[configCtx.Key]
	if !ok {
		return nil, axerror.ERR_API_RESOURCE_NOT_FOUND.NewWithMessagef("Configuration exists under user %s, name %s but does not have key %s", configCtx.User, configCtx.Name, configCtx.Key)
	}
	if config.ConfigurationIsSecrets {
		// Secret substitution is handled at platform
		return nil, nil
	}
	return &configVal, nil
}

func CreateKubernetesSecret(config *ConfigurationData) *axerror.AXError {
	// Make sure log not printing out the secret content
	utils.InfoLog.Println("[AXMON] Creating kube secret")
	secret := map[string]interface{}{
		"namespace": config.ConfigurationUser,
		"name":      config.ConfigurationName,
		"data":      config.ConfigurationValue,
	}

	axErr, _ := utils.AxmonCl.PostWithTimeRetry("secret", nil, secret, nil, retryConfig)

	if axErr != nil {
		return axErr
	}
	return nil
}

func DeleteKubernetesSecret(config *ConfigurationData) *axerror.AXError {
	// Make sure log not printing out the secret content
	utils.InfoLog.Println("[AXMON] Deleting kube secret")
	del_secret_url := fmt.Sprintf("secret/%s/%s", config.ConfigurationUser, config.ConfigurationName)

	axErr, _ := utils.AxmonCl.DeleteWithTimeRetry(del_secret_url, nil, nil, nil, retryConfig)

	if axErr != nil {
		return axErr
	}
	return nil
}
