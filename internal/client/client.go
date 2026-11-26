package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mdp/qrterminal"

	"go.mau.fi/whatsmeow"
	wmstore "go.mau.fi/whatsmeow/store"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/ratelimit"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/stt"
)

type Config struct {
	StoreDir          string             // directory holding messages.db and whatsapp.db
	Store             *store.Store       // initialized message store
	Logger            waLog.Logger       // optional; defaults to stderr at INFO
	AllowedMediaRoot  string             // absolute path; media_path args must live under this
	Redactor          *security.Redactor // optional; defaults to a redacting instance
	DeviceName        string
	RateLimiter       *ratelimit.Limiter
	MetricSink        MetricSink
	OnIncomingMessage func(data string)
	STTClient         stt.Client
}

type MetricSink interface {
	IncMessagesIn()
	IncMessagesOut()
	IncSends()
	IncErrors()
	IncRateLimited()
}

type handlerInstallState int

const (
	handlerNotInstalled handlerInstallState = iota
	handlerInstalled
)

type Client struct {
	wa               *whatsmeow.Client
	downloadMedia    func(context.Context, whatsmeow.DownloadableMessage) ([]byte, error)
	store            *store.Store
	log              waLog.Logger
	handlerID        uint32
	handlerState     handlerInstallState
	allowedMediaRoot string
	redactor         *security.Redactor
	limiter          *ratelimit.Limiter
	metricSink       MetricSink
	onIncomingMsg    func(data string)
	sttClient        stt.Client

	presenceMu      sync.Mutex
	presenceWaiters map[string][]chan PresenceResult

	ownPresenceMu sync.Mutex
	ownPresence   types.Presence
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Store == nil {
		return nil, errors.New("client: Config.Store is required")
	}
	if cfg.StoreDir == "" {
		return nil, errors.New("client: Config.StoreDir is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = NewStderrLogger("Client", "INFO", true)
	}

	redactor := cfg.Redactor
	if redactor == nil {
		redactor = &security.Redactor{}
	}

	limiter := cfg.RateLimiter
	if limiter == nil {
		limiter = ratelimit.New(ratelimit.DefaultConfig())
	}

	if latest, err := whatsmeow.GetLatestVersion(ctx, nil); err != nil {
		logger.Warnf("Failed to get latest WhatsApp version: %v (using default)", err)
	} else if latest != nil {
		wmstore.SetWAVersion(*latest)
		logger.Infof("Using WhatsApp version: %s", latest.String())
	}

	if err := os.MkdirAll(cfg.StoreDir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := store.ChmodBestEffort(cfg.StoreDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod store dir: %w", err)
	}

	dbLog := NewStderrLogger("Database", "INFO", true)
	waPath := filepath.Join(cfg.StoreDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+waPath+"?_foreign_keys=on", dbLog)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sqlstore: %w", err)
	}
	if err := store.ChmodBestEffort(waPath, 0o600); err != nil {
		return nil, fmt.Errorf("chmod whatsapp.db: %w", err)
	}

	if cfg.DeviceName != "" {
		wmstore.DeviceProps.Os = proto.String(cfg.DeviceName)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			device = container.NewDevice()
			logger.Infof("Created new device")
		} else {
			return nil, fmt.Errorf("get device: %w", err)
		}
	}

	wa := whatsmeow.NewClient(device, logger)
	if wa == nil {
		return nil, errors.New("failed to create whatsmeow client")
	}

	return &Client{
		wa:               wa,
		downloadMedia:    wa.Download,
		store:            cfg.Store,
		log:              logger,
		allowedMediaRoot: cfg.AllowedMediaRoot,
		redactor:         redactor,
		limiter:          limiter,
		metricSink:       cfg.MetricSink,
		onIncomingMsg:    cfg.OnIncomingMessage,
		sttClient:        cfg.STTClient,
		ownPresence:      types.PresenceUnavailable,
	}, nil
}

func (c *Client) incMessagesIn() {
	if c.metricSink != nil {
		c.metricSink.IncMessagesIn()
	}
}

func (c *Client) incMessagesOut() {
	if c.metricSink != nil {
		c.metricSink.IncMessagesOut()
	}
}

func (c *Client) incSends() {
	if c.metricSink != nil {
		c.metricSink.IncSends()
	}
}

func (c *Client) incErrors() {
	if c.metricSink != nil {
		c.metricSink.IncErrors()
	}
}

func (c *Client) incRateLimited() {
	if c.metricSink != nil {
		c.metricSink.IncRateLimited()
	}
}

func (c *Client) SetMetricSink(sink MetricSink) {
	c.metricSink = sink
}

func (c *Client) SetOnIncomingMessage(fn func(data string)) {
	c.onIncomingMsg = fn
}

func (c *Client) ValidateMediaPath(userPath string) (string, error) {
	return security.ValidateMediaPath(userPath, c.allowedMediaRoot)
}

func (c *Client) ValidateOutputPath(userPath string) (string, error) {
	return security.ValidateOutputPath(userPath, c.allowedMediaRoot)
}

func (c *Client) IsLoggedIn() bool {
	return c.wa.Store.ID != nil
}

// QRChannel exposes whatsmeow's pairing QR channel. Must be called before
// Connect when the device is not yet paired. The channel emits QRChannelItem
func (c *Client) QRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return c.wa.GetQRChannel(ctx)
}

func (c *Client) Logout(ctx context.Context) error {
	return c.wa.Logout(ctx)
}

type ConnectOpts struct {
	AllowUnpaired bool
}

var errNotPaired = errors.New("device not paired; run login first")

func (c *Client) Connect(ctx context.Context, opts ConnectOpts) error {
	if !opts.AllowUnpaired && c.wa.Store.ID == nil {
		return errNotPaired
	}
	return c.wa.ConnectContext(ctx)
}

func (c *Client) Login(ctx context.Context, qrOut io.Writer) error {
	if c.wa.Store.ID != nil {
		return c.wa.ConnectContext(ctx)
	}

	qrChan, err := c.wa.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := c.wa.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return errors.New("QR channel closed before pairing completed")
			}
			switch evt.Event {
			case "code":
				fmt.Fprintln(qrOut, "\nScan this QR code with your WhatsApp app:")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, qrOut)
			case "success":
				time.Sleep(500 * time.Millisecond)
				return nil
			case "timeout":
				return errors.New("QR code scan timed out")
			default:
				if evt.Event == "" {
					continue
				}
				c.log.Warnf("QR pairing event: %s", evt.Event)
			}
		}
	}
}

func (c *Client) PairPhoneLogin(ctx context.Context, phone string, codeOut io.Writer) error {
	if c.wa.Store.ID != nil {
		return c.wa.ConnectContext(ctx)
	}

	qrChan, err := c.wa.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := c.wa.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	code, err := c.PairPhone(ctx, phone)
	if err != nil {
		return err
	}
	fmt.Fprintf(codeOut, "\nEnter this code in WhatsApp (Settings → Linked Devices → Link a Device → Link with phone number instead): %s\n", FormatPairingCode(code))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return errors.New("QR channel closed before pairing completed")
			}
			switch evt.Event {
			case "success":
				time.Sleep(500 * time.Millisecond)
				return nil
			case "timeout":
				return errors.New("pairing code entry timed out")
			case "", "code":
			default:
				c.log.Warnf("pairing event: %s", evt.Event)
			}
		}
	}
}

func (c *Client) Disconnect() {
	if c.wa != nil {
		c.wa.Disconnect()
	}
}

func (c *Client) IsConnected() bool {
	return c.wa.IsConnected()
}

func (c *Client) AddLoggedOutHandler(fn func(*events.LoggedOut)) {
	c.wa.AddEventHandler(func(evt interface{}) {
		if lo, ok := evt.(*events.LoggedOut); ok {
			fn(lo)
		}
	})
}

func (c *Client) WA() *whatsmeow.Client { return c.wa }

func (c *Client) Store() *store.Store { return c.store }

func (c *Client) GetPendingScheduledMessages(ctx context.Context) ([]store.ScheduledMessage, error) {
	return c.store.GetPendingScheduledMessages(ctx)
}

func (c *Client) MarkMessageSent(id, messageID string, sentAt time.Time) error {
	return c.store.MarkMessageSent(id, messageID, sentAt)
}

func (c *Client) MarkMessageFailed(id, reason string) error {
	return c.store.MarkMessageFailed(id, reason)
}

type ConnectionState int

const (
	ConnectionStateUnpaired     ConnectionState = iota // no stored session
	ConnectionStateDisconnected                        // paired but socket down
	ConnectionStateConnected                           // paired and socket up
)

func (c *Client) ConnectionState() ConnectionState {
	if c.wa.Store.ID == nil {
		return ConnectionStateUnpaired
	}
	if !c.wa.IsConnected() {
		return ConnectionStateDisconnected
	}
	return ConnectionStateConnected
}
