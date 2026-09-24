package api

type createOrderRequest struct {
	AmountUSD      string `json:"amount_usd"`
	PreferredChain string `json:"chain,omitempty"`
}

type orderResponse struct {
	OrderID        string `json:"order_id"`
	Chain          string `json:"chain"`
	Token          string `json:"token"`
	DepositAddress string `json:"deposit_address"`
	QRString       string `json:"qr_string"`
	AmountUSD      string `json:"amount_usd"`
	AmountToken    string `json:"amount_token"`
	Status         string `json:"status"`
	TxHash         string `json:"tx_hash,omitempty"`
	Confirmations  int64  `json:"confirmations"`
	RequiredConfs  int64  `json:"required_confirmations"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	ConfirmedAt    string `json:"confirmed_at,omitempty"`
}

type registerWebhookRequest struct {
	URL string `json:"url"`
}

type registerWebhookResponse struct {
	WebhookSecret string `json:"webhook_secret"`
}

type errorResponse struct {
	Error string `json:"error"`
}
