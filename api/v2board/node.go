package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"encoding/json"
)

// Security type
const (
	None    = 0
	Tls     = 1
	Reality = 2
)

type NodeInfo struct {
	Id           int
	Type         string
	Security     int
	PushInterval time.Duration
	PullInterval time.Duration
	Tag          string
	Common       *CommonNode
}

type CommonNode struct {
	Protocol   string      `json:"protocol"`
	ListenIP   string      `json:"listen_ip"`
	ServerPort int         `json:"server_port"`
	Routes     []Route     `json:"routes"`
	BaseConfig *BaseConfig `json:"base_config"`
	//vless vmess trojan
	Tls                int         `json:"tls"`
	TlsSettings        TlsSettings `json:"tls_settings"`
	CertInfo           *CertInfo
	Network            string          `json:"network"`
	NetworkSettings    json.RawMessage `json:"network_settings"`
	Encryption         string          `json:"encryption"`
	EncryptionSettings EncSettings     `json:"encryption_settings"`
	ServerName         string          `json:"server_name"`
	Flow               string          `json:"flow"`
	//shadowsocks
	Cipher    string `json:"cipher"`
	ServerKey string `json:"server_key"`
	//tuic
	CongestionControl string `json:"congestion_control"`
	ZeroRTTHandshake  bool   `json:"zero_rtt_handshake"`
	//anytls
	PaddingScheme []string `json:"padding_scheme,omitempty"`
	//hysteria hysteria2
	UpMbps                  int    `json:"up_mbps"`
	DownMbps                int    `json:"down_mbps"`
	Obfs                    string `json:"obfs"`
	ObfsPassword            string `json:"obfs-password"`
	Ignore_Client_Bandwidth bool   `json:"ignore_client_bandwidth"`
}

type Route struct {
	Id          int      `json:"id"`
	Match       []string `json:"match"`
	Action      string   `json:"action"`
	ActionValue *string  `json:"action_value"`
}

type BaseConfig struct {
	PushInterval           any `json:"push_interval"`
	PullInterval           any `json:"pull_interval"`
	DeviceOnlineMinTraffic int `json:"device_online_min_traffic"`
	NodeReportMinTraffic   int `json:"node_report_min_traffic"`
}

type TlsSettings struct {
	ServerName       string `json:"server_name"`
	Dest             string `json:"dest"`
	ServerPort       string `json:"server_port"`
	ShortId          string `json:"short_id"`
	PrivateKey       string `json:"private_key"`
	Mldsa65Seed      string `json:"mldsa65Seed"`
	Xver             uint64 `json:"xver,string"`
	CertMode         string `json:"cert_mode"`
	CertFile         string `json:"cert_file"`
	KeyFile          string `json:"key_file"`
	Provider         string `json:"provider"`
	DNSEnv           string `json:"dns_env"`
	RejectUnknownSni string `json:"reject_unknown_sni"`
}

type CertInfo struct {
	CertMode         string
	CertFile         string
	KeyFile          string
	Email            string
	CertDomain       string
	DNSEnv           map[string]string
	Provider         string
	RejectUnknownSni bool
}

type EncSettings struct {
	Mode          string `json:"mode"`
	Ticket        string `json:"ticket"`
	ServerPadding string `json:"server_padding"`
	PrivateKey    string `json:"private_key"`
}

const defaultNodeInterval = 60 * time.Second

func (c *Client) GetNodeInfo() (node *NodeInfo, err error) {
	const path = "/api/v2/server/config"
	r, err := c.client.
		R().
		SetHeader("If-None-Match", c.nodeEtag).
		ForceContentType("application/json").
		Get(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("received nil response")
	}
	if r.StatusCode() >= 400 {
		return nil, fmt.Errorf("node info response status code: %d", r.StatusCode())
	}

	if r.StatusCode() == 304 {
		return nil, nil
	}
	hash := sha256.Sum256(r.Body())
	newBodyHash := hex.EncodeToString(hash[:])
	if c.responseBodyHash == newBodyHash {
		return nil, nil
	}
	c.responseBodyHash = newBodyHash
	c.nodeEtag = r.Header().Get("ETag")

	defer func() {
		if r.RawBody() != nil {
			r.RawBody().Close()
		}
	}()
	if len(r.Body()) == 0 {
		return nil, fmt.Errorf("received empty node info response body")
	}
	node = &NodeInfo{
		Id: c.NodeId,
	}
	// parse protocol params
	cm := &CommonNode{}
	err = json.Unmarshal(r.Body(), cm)
	if err != nil {
		return nil, fmt.Errorf("decode node params error: %s", err)
	}
	switch cm.Protocol {
	case "vmess", "trojan", "hysteria2", "tuic", "anytls", "vless":
		node.Type = cm.Protocol
		node.Security = cm.Tls
	case "shadowsocks":
		node.Type = cm.Protocol
		node.Security = 0
	default:
		return nil, fmt.Errorf("unsupport protocol: %s", cm.Protocol)
	}
	node.Tag = fmt.Sprintf("[%s]-%s:%d", c.APIHost, node.Type, node.Id)
	cf := cm.TlsSettings.CertFile
	kf := cm.TlsSettings.KeyFile
	if cf == "" {
		cf = filepath.Join("/etc/v2node/", cm.Protocol+strconv.Itoa(c.NodeId)+".cer")
	}
	if kf == "" {
		kf = filepath.Join("/etc/v2node/", cm.Protocol+strconv.Itoa(c.NodeId)+".key")
	}
	cm.CertInfo = &CertInfo{
		CertMode:         cm.TlsSettings.CertMode,
		CertFile:         cf,
		KeyFile:          kf,
		Email:            "node@v2board.com",
		CertDomain:       cm.TlsSettings.ServerName,
		DNSEnv:           make(map[string]string),
		Provider:         cm.TlsSettings.Provider,
		RejectUnknownSni: cm.TlsSettings.RejectUnknownSni == "1",
	}
	if cm.CertInfo.CertMode == "dns" && cm.TlsSettings.DNSEnv != "" {
		envs := strings.Split(cm.TlsSettings.DNSEnv, ",")
		for _, env := range envs {
			kv := strings.SplitN(env, "=", 2)
			if len(kv) == 2 {
				cm.CertInfo.DNSEnv[kv[0]] = kv[1]
			}
		}
	}

	// set interval
	if cm.BaseConfig == nil {
		node.PushInterval = defaultNodeInterval
		node.PullInterval = defaultNodeInterval
	} else {
		node.PushInterval = intervalToTime(cm.BaseConfig.PushInterval)
		node.PullInterval = intervalToTime(cm.BaseConfig.PullInterval)
	}

	node.Common = cm

	return node, nil
}

func intervalToTime(i interface{}) time.Duration {
	switch v := i.(type) {
	case nil:
		return defaultNodeInterval
	case int:
		if v <= 0 {
			return defaultNodeInterval
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return defaultNodeInterval
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return defaultNodeInterval
		}
		return time.Duration(v) * time.Second
	case float64:
		if v <= 0 {
			return defaultNodeInterval
		}
		return time.Duration(v) * time.Second
	case string:
		sec, err := strconv.Atoi(v)
		if err != nil || sec <= 0 {
			return defaultNodeInterval
		}
		return time.Duration(sec) * time.Second
	default:
		return defaultNodeInterval
	}
}
