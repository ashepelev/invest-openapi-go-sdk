package sdk

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

const StreamingApiURL = "wss://invest-public-api.tinkoff.ru/openapi/md/v1/md-openapi/ws"

const DefaultPongWait = 60 * time.Second
const DefaultPingPeriod = 54 * time.Second

type Logger interface {
	Printf(format string, args ...interface{})
}

type PingPongConfig struct {
	isEnabled  bool
	pongWait   time.Duration
	pingPeriod time.Duration
}

type StreamingClient struct {
	logger Logger
	conn   *websocket.Conn
	token  string
	apiURL string

	pingPongCfg *PingPongConfig
	pingTicker  *time.Ticker
}

func NewStreamingClient(logger Logger, token string) (*StreamingClient, error) {
	return NewStreamingClientCustom(logger, token, StreamingApiURL)
}

func NewStreamingClientCustom(logger Logger, token, apiURL string) (*StreamingClient, error) {
	return NewStreamingClientCustomPingPong(logger, token, apiURL, &PingPongConfig{false, DefaultPongWait, DefaultPingPeriod})
}

func NewStreamingClientCustomPingPong(logger Logger, token, apiURL string, pingPongCfg *PingPongConfig) (*StreamingClient, error) {
	client := &StreamingClient{
		logger: logger,
		token:  token,
		apiURL: apiURL,

		pingPongCfg: pingPongCfg,
	}

	conn, err := client.connect()
	if err != nil {
		return nil, err
	}
	client.conn = conn

	return client, nil
}

func (c *StreamingClient) Close() error {
	c.pingTicker.Stop()

	return c.conn.Close()
}

func (c *StreamingClient) RunReadLoop(fn func(event interface{}) error) error {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return errors.Wrap(err, "can't read message")
		}

		var event Event
		if err := json.Unmarshal(msg, &event); err != nil {
			c.logger.Printf("Can't unmarshal event %s", msg)
			continue
		}

		switch event.Name {
		case "candle":
			var event CandleEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				c.logger.Printf("Can't unmarshal event candle %s", msg)
				continue
			}
			if err := fn(event); err != nil {
				return err
			}
		case "orderbook":
			var event OrderBookEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				c.logger.Printf("Can't unmarshal event orderbook %s", msg)
				continue
			}
			if err := fn(event); err != nil {
				return err
			}
		case "instrument_info":
			var event InstrumentInfoEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				c.logger.Printf("Can't unmarshal event instrument_info %s", msg)
				continue
			}
			if err := fn(event); err != nil {
				return err
			}
		case "error":
			var event ErrorEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				c.logger.Printf("Can't unmarshal event error %s", msg)
				continue
			}
			if err := fn(event); err != nil {
				return err
			}
		default:
			c.logger.Printf("Get unknown event %s", msg)
		}
	}
}

func (c *StreamingClient) SubscribeCandle(figi string, interval CandleInterval, requestID string) error {
	sub := `{ "event": "candle:subscribe", "request_id": "` + requestID + `", "figi": "` + figi + `", "interval": "` + string(interval) + `"}`

	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't subscribe to event")
	}

	return nil
}

func (c *StreamingClient) UnsubscribeCandle(figi string, interval CandleInterval, requestID string) error {
	sub := `{ "event": "candle:unsubscribe", "request_id": "` + requestID + `", "figi": "` + figi + `", "interval": "` + string(interval) + `"}`
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't unsubscribe from event")
	}

	return nil
}

func (c *StreamingClient) SubscribeOrderbook(figi string, depth int, requestID string) error {
	if depth < 1 || depth > MaxOrderbookDepth {
		return ErrDepth
	}

	sub := `{ "event": "orderbook:subscribe", "request_id": "` + requestID + `", "figi": "` + figi + `", "depth": ` + strconv.Itoa(depth) + `}`
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't subscribe to event")
	}

	return nil
}

func (c *StreamingClient) UnsubscribeOrderbook(figi string, depth int, requestID string) error {
	if depth < 1 || depth > MaxOrderbookDepth {
		return ErrDepth
	}

	sub := `{ "event": "orderbook:unsubscribe", "request_id": "` + requestID + `", "figi": "` + figi + `", "depth": ` + strconv.Itoa(depth) + `}`
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't unsubscribe from event")
	}

	return nil
}

func (c *StreamingClient) SubscribeInstrumentInfo(figi, requestID string) error {
	sub := `{"event": "instrument_info:subscribe", "request_id": "` + requestID + `", "figi": "` + figi + `"}`
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't subscribe to event")
	}

	return nil
}

func (c *StreamingClient) UnsubscribeInstrumentInfo(figi, requestID string) error {
	sub := `{"event": "instrument_info:unsubscribe", "request_id": "` + requestID + `", "figi": "` + figi + `"}`
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return errors.Wrap(err, "can't unsubscribe from event")
	}

	return nil
}

var ErrForbidden = errors.New("invalid token")
var ErrUnauthorized = errors.New("token not provided")

func (c *StreamingClient) connect() (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 5 * time.Second,
	}

	conn, resp, err := dialer.Dial(c.apiURL, http.Header{"Authorization": {"Bearer " + c.token}})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusForbidden {
				return nil, ErrForbidden
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, ErrUnauthorized
			}

			return nil, errors.Wrapf(err, "can't connect to %s %s", c.apiURL, resp.Status)
		}
		return nil, errors.Wrapf(err, "can't connect to %s", c.apiURL)
	}
	defer resp.Body.Close()

	if c.pingPongCfg.isEnabled {
		conn.SetReadDeadline(time.Now().Add(c.pingPongCfg.pongWait))

		conn.SetPingHandler(func(message string) error {
			err := conn.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(time.Second))
			if err == websocket.ErrCloseSent {
				return nil
			} else if e, ok := err.(net.Error); ok && e.Temporary() {
				return nil
			}
			return err
		})

		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(c.pingPongCfg.pongWait))
			return nil
		})

		c.pingTicker = time.NewTicker(c.pingPongCfg.pingPeriod)

		go func() {
			<-c.pingTicker.C

			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}()
	}

	return conn, nil
}
