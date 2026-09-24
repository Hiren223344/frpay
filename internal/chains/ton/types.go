package ton

// jettonTransfersResponse mirrors toncenter v3's documented
// GET /jetton/transfers shape. TON's public API surface moves faster
// than most chains' — if toncenter changes field names, this is the
// only file that needs updating.
type jettonTransfersResponse struct {
	JettonTransfers []jettonTransfer `json:"jetton_transfers"`
}

type jettonTransfer struct {
	// Amount is the raw Jetton amount as a decimal string (USDT has 6
	// decimals on TON, same as TRC20/ERC20 USDT).
	Amount string `json:"amount"`
	// TransactionHash identifies the transfer.
	TransactionHash string `json:"transaction_hash"`
	// TransactionLt is TON's logical time for the transaction: a
	// per-account monotonically increasing counter, used here as the
	// "block height" cursor analog.
	TransactionLt string `json:"transaction_lt"`
	// TransactionNow is the transfer's unix timestamp.
	TransactionNow int64 `json:"transaction_now"`
	// Destination is the recipient Jetton-wallet address. toncenter
	// already filters server-side by owner_address, so every entry
	// returned is one of ours — this is kept only for logging.
	Destination string `json:"destination"`
}
