package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/protobuf/proto"
)

// Client envuelve el cliente de whatsmeow y expone solo lo que la app necesita.
type Client struct {
	wa        *whatsmeow.Client
	container *sqlstore.Container

	mu        sync.Mutex
	cond      *sync.Cond
	currentQR string
	qrVersion int // se incrementa cada vez que currentQR cambia (nuevo código o login exitoso)
	loggingIn bool
}

// New crea el cliente y abre (o crea) la sesión en SQLite.
// Si no hay sesión guardada, lanza el login por QR en segundo plano:
// el servidor HTTP arranca igual, y el QR se consulta con GET /login/qr.
func New(ctx context.Context, dbPath string) (*Client, error) {
	// Personalizar el nombre e icono que aparecerá en WhatsApp ("Dispositivos vinculados")
	store.DeviceProps.Os = proto.String("AppRFE Logística")
	store.DeviceProps.PlatformType = waE2E.DeviceProps_CHROME.Enum()

	dbLog := waLog.Stdout("Database", "WARN", true)

	container, err := sqlstore.New(ctx, "sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on", dbPath), dbLog)
	if err != nil {
		return nil, fmt.Errorf("abriendo store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("obteniendo device: %w", err)
	}

	clientLog := waLog.Stdout("Client", "WARN", true)
	waClient := whatsmeow.NewClient(deviceStore, clientLog)
	waClient.AddEventHandler(handleEvent)

	c := &Client{
		wa:        waClient,
		container: container,
	}
	c.cond = sync.NewCond(&c.mu)

	if waClient.Store.ID == nil {
		// No hay sesión guardada todavía: no bloqueamos el arranque del server.
		_ = c.StartLogin(ctx)
	} else {
		if err := waClient.Connect(); err != nil {
			return nil, fmt.Errorf("conectando: %w", err)
		}
	}

	return c, nil
}

// StartLogin lanza (o relanza) el flujo de QR en segundo plano, sin bloquear
// al llamador. Si ya hay sesión activa o un login en curso, devuelve error.
func (c *Client) StartLogin(ctx context.Context) error {
	c.mu.Lock()
	if c.wa == nil {
		c.mu.Unlock()
		return fmt.Errorf("cliente no inicializado")
	}
	if c.wa.Store != nil && c.wa.Store.ID != nil {
		c.mu.Unlock()
		return fmt.Errorf("ya hay una sesión activa")
	}
	if c.loggingIn {
		c.mu.Unlock()
		return nil
	}
	c.loggingIn = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.loggingIn = false
			c.mu.Unlock()
		}()

		bgCtx := context.Background()

		if c.wa.IsConnected() {
			c.wa.Disconnect()
		}

		qrChan, err := c.wa.GetQRChannel(bgCtx)
		if err != nil {
			fmt.Println("Error obteniendo QRChannel:", err)
			return
		}

		if err := c.wa.Connect(); err != nil {
			fmt.Println("Error conectando socket para QR:", err)
			return
		}

		for evt := range qrChan {
			switch evt.Event {
			case "code":
				c.setQR(evt.Code)
			case "success":
				c.setQR("")
			case "timeout":
				c.setQR("")
			}
		}
	}()

	return nil
}

// setQR actualiza el código vigente, sube la versión y despierta a quien
// esté esperando en WaitForQR.
func (c *Client) setQR(code string) {
	c.mu.Lock()
	c.currentQR = code
	c.qrVersion++
	c.mu.Unlock()
	c.cond.Broadcast()
}

// WaitForQR bloquea hasta que la versión del QR cambie respecto a "since",
// o hasta que el ctx se cancele/expire (lo que pase primero).
// Devuelve el código vigente (puede ser "" si el login ya tuvo éxito o si
// nunca llegó a generarse antes del timeout) y la versión actual.
func (c *Client) WaitForQR(ctx context.Context, since int) (code string, version int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Goroutine "despertadora": cuando el ctx expira, hace Broadcast para
	// que el Wait() de abajo no se quede colgado para siempre.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		case <-stop:
		}
	}()

	for c.qrVersion == since && ctx.Err() == nil {
		c.cond.Wait()
	}

	return c.currentQR, c.qrVersion
}

// IsConnected indica si el socket WebSocket está abierto. OJO: esto se
// vuelve true apenas se llama Connect() para pedir el QR, ANTES de escanear
// nada — no lo uses para saber si ya se puede mandar mensajes.
func (c *Client) IsConnected() bool {
	return c.wa != nil && c.wa.IsConnected()
}

// IsLoggedIn indica si la sesión ya está autenticada y lista para enviar
// mensajes: requiere un dispositivo vinculado (Store.ID != nil).
func (c *Client) IsLoggedIn() bool {
	if c.wa == nil || c.wa.Store == nil || c.wa.Store.ID == nil {
		return false
	}
	if !c.wa.IsConnected() {
		_ = c.wa.Connect()
	}
	return true
}

// Disconnect cierra la conexión de forma limpia.
func (c *Client) Disconnect() {
	if c.wa != nil {
		c.wa.Disconnect()
	}
}

// SendText envía un mensaje de texto simple a un número.
// number debe venir sin "+", con código de país incluido, ej: "51987654321".
func (c *Client) SendText(ctx context.Context, number, text string) (messageID string, err error) {
	if !c.IsLoggedIn() {
		return "", fmt.Errorf("cliente no conectado")
	}

	number = strings.ReplaceAll(number, "-", "")
	number = strings.ReplaceAll(number, " ", "")
	number = strings.TrimPrefix(strings.TrimSpace(number), "+")
	recipient := types.JID{
		User:   number,
		Server: types.DefaultUserServer,
	}

	msg := &waE2E.Message{
		Conversation: proto.String(text),
	}

	resp, err := c.wa.SendMessage(ctx, recipient, msg)
	if err != nil {
		return "", err
	}

	return resp.ID, nil
}

// SendDocument sube y envía un documento (PDF, etc.) a un número de WhatsApp.
func (c *Client) SendDocument(ctx context.Context, number, fileName string, fileData []byte, mimeType, caption string) (messageID string, err error) {
	if !c.IsLoggedIn() {
		return "", fmt.Errorf("cliente no conectado")
	}

	number = strings.ReplaceAll(number, "-", "")
	number = strings.ReplaceAll(number, " ", "")
	number = strings.TrimPrefix(strings.TrimSpace(number), "+")

	recipient := types.JID{
		User:   number,
		Server: types.DefaultUserServer,
	}

	if mimeType == "" {
		mimeType = "application/pdf"
	}
	if fileName == "" {
		fileName = "documento.pdf"
	}

	// Subir archivo multimedia al servidor de WhatsApp
	resp, err := c.wa.Upload(ctx, fileData, whatsmeow.MediaDocument)
	if err != nil {
		return "", fmt.Errorf("error subiendo archivo a WhatsApp: %w", err)
	}

	docMsg := &waE2E.DocumentMessage{
		URL:           proto.String(resp.URL),
		DirectPath:    proto.String(resp.DirectPath),
		MediaKey:      resp.MediaKey,
		Mimetype:      proto.String(mimeType),
		Title:         proto.String(fileName),
		FileName:      proto.String(fileName),
		FileSHA256:    resp.FileSHA256,
		FileEncSHA256: resp.FileEncSHA256,
		FileLength:    proto.Uint64(resp.FileLength),
	}

	if caption != "" {
		docMsg.Caption = proto.String(caption)
	}

	msg := &waE2E.Message{
		DocumentMessage: docMsg,
	}

	sendResp, err := c.wa.SendMessage(ctx, recipient, msg)
	if err != nil {
		return "", err
	}

	return sendResp.ID, nil
}

// Logout desvincula la sesión activa de WhatsApp, revoca las credenciales del dispositivo y reinicia el cliente.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wa == nil {
		return fmt.Errorf("cliente no inicializado")
	}

	if c.wa.IsLoggedIn() {
		_ = c.wa.Logout(ctx)
	} else if c.wa.IsConnected() {
		c.wa.Disconnect()
	}

	if c.wa.Store != nil {
		_ = c.wa.Store.Delete(ctx)
	}

	if c.container != nil {
		deviceStore, err := c.container.GetFirstDevice(ctx)
		if err == nil {
			clientLog := waLog.Stdout("Client", "WARN", true)
			c.wa = whatsmeow.NewClient(deviceStore, clientLog)
			c.wa.AddEventHandler(handleEvent)
		}
	}

	c.currentQR = ""
	c.qrVersion++
	c.loggingIn = false
	c.cond.Broadcast()

	return nil
}

func handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		fmt.Println("Mensaje recibido:", v.Message.GetConversation())
	}
}
