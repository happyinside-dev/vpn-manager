package model

type User struct {
	CanRotate        bool
	CanManageUsers   bool
	CreatedBy        int64
	Name             string
	Token            string
	Enabled          bool
	VLESSUUID        string
	HysteriaPassword string
	TelegramID       int64
}

type ServerConfig struct {
	Host     string
	VLESS    VLESSConfig
	Hysteria HysteriaConfig
}

type VLESSConfig struct {
	Enabled     bool
	Port        int
	SNI         string
	PublicKey   string
	ShortID     string
	Flow        string
	Fingerprint string
}

type HysteriaConfig struct {
	Enabled   bool
	Port      int
	SNI       string
	Insecure  bool
	PinSHA256 string
}
