package api

type createOrderRequest struct {
	UserID        string `json:"user_id"`
	BaseAmountUSD string `json:"base_amount_usd"`
	Chain         string `json:"chain"`
}

type orderResponse struct {
	OrderID        string `json:"order_id"`
	UserID         string `json:"user_id"`
	Chain          string `json:"chain"`
	Token          string `json:"token"`
	Address        string `json:"address"`
	QRString       string `json:"qr_string"`
	BaseAmountUSD  string `json:"base_amount_usd"`
	ExpectedAmount string `json:"expected_amount"`
	ReceivedAmount string `json:"received_amount,omitempty"`
	Status         string `json:"status"`
	TxHash         string `json:"tx_hash,omitempty"`
	Confirmations  int64  `json:"confirmations"`
	RequiredConfs  int64  `json:"required_confirmations"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	ConfirmedAt    string `json:"confirmed_at,omitempty"`
}

type chainInfo struct {
	Chain                 string `json:"chain"`
	Address               string `json:"address"`
	Token                 string `json:"token"`
	Decimals              int32  `json:"decimals"`
	RequiredConfirmations int64  `json:"required_confirmations"`
}

type errorResponse struct {
	Error string `json:"error"`
}
