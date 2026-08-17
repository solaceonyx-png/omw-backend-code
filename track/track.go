package track

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

//encore:service
type Service struct {
	db *gorm.DB
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // Allow Angular local dev
}

type LocationUpdate struct {
	RideID    string  `json:"rideId"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Bearing   float64 `json:"bearing"` // Angle in degrees to rotate the car icon
}

var (
	clients   = make(map[*websocket.Conn]bool)
	broadcast = make(chan LocationUpdate)
	mutex     = sync.Mutex{}
)

// func (s *Service) StreamRide(w http.ResponseWriter, req *http.Request) {
//
//encore:api public raw method=GET path=/track/stream
func StreamRide(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	mutex.Lock()
	clients[conn] = true
	mutex.Unlock()

	for {
		var update LocationUpdate
		err := conn.ReadJSON(&update)
		if err != nil {
			mutex.Lock()
			delete(clients, conn)
			mutex.Unlock()
			break
		}
		fmt.Println("reached here")
		fmt.Println(update)
		// Broadcast incoming driver coordinates to all connected clients (passenger & map views)
		broadcast <- update
	}
}

// Background worker to push incoming location to all connected sockets
func init() {
	go func() {
		for {
			update := <-broadcast
			mutex.Lock()
			for client := range clients {
				_ = client.WriteJSON(update)
			}
			mutex.Unlock()
		}
	}()
}
