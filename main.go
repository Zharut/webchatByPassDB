package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var db *sql.DB
var mu sync.Mutex

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var clients = make(map[*websocket.Conn]bool)
var broadcast = make(chan Message)

type Room struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Creator string `json:"creator"`
}

type Message struct {
	ID     int       `json:"id"`
	RoomID int       `json:"room_id"`
	Sender string    `json:"sender"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

func main() {
    var err error
    db, err = sql.Open("sqlite3", "./chat.db")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

	

    createTables()

    router := mux.NewRouter()
    router.HandleFunc("/login", loginHandler).Methods("POST")
    router.HandleFunc("/rooms", roomsHandler).Methods("GET", "POST")
    router.HandleFunc("/rooms/{id}", deleteRoomHandler).Methods("DELETE")
    router.HandleFunc("/rooms/{id}/messages", messagesHandler).Methods("GET", "POST")
	router.HandleFunc("/ws", handleConnections)

	go handleMessages()

    // Serve static files
    router.PathPrefix("/").Handler(http.StripPrefix("/", http.FileServer(http.Dir("./static/"))))

    log.Println("Server started on :8080")
    http.ListenAndServe(":8080", router)
}

func createTables() {
	db.Exec(`CREATE TABLE IF NOT EXISTS rooms (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, creator TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS messages (id INTEGER PRIMARY KEY AUTOINCREMENT, room_id INTEGER, sender TEXT, text TEXT, time TIMESTAMP, FOREIGN KEY(room_id) REFERENCES rooms(id))`)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	var user struct{ Name string }
	json.NewDecoder(r.Body).Decode(&user)
	if user.Name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func roomsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var room Room
		json.NewDecoder(r.Body).Decode(&room)
		if room.Name == "" || room.Creator == "" {
			http.Error(w, "Room name and creator are required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result, _ := db.Exec("INSERT INTO rooms (name, creator) VALUES (?, ?)", room.Name, room.Creator)
	lastID, err := result.LastInsertId()
	if err != nil {
    	http.Error(w, "Failed to create room", http.StatusInternalServerError)
    	return
	}
	room.ID = int(lastID)

		json.NewEncoder(w).Encode(room)
	} else {
		rows, _ := db.Query("SELECT id, name, creator FROM rooms")
		defer rows.Close()
		var rooms []Room
		for rows.Next() {
			var room Room
			rows.Scan(&room.ID, &room.Name, &room.Creator)
			rooms = append(rooms, room)
		}
		json.NewEncoder(w).Encode(rooms)
	}
}

func deleteRoomHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	mu.Lock()
	defer mu.Unlock()
	db.Exec("DELETE FROM rooms WHERE id = ?", id)
	db.Exec("DELETE FROM messages WHERE room_id = ?", id)
	w.WriteHeader(http.StatusNoContent)
}

func messagesHandler(w http.ResponseWriter, r *http.Request) {
	roomID := mux.Vars(r)["id"]
	if r.Method == "POST" {
		var msg Message
		json.NewDecoder(r.Body).Decode(&msg)
		msg.Time = time.Now()
		mu.Lock()
		defer mu.Unlock()
		db.Exec("INSERT INTO messages (room_id, sender, text, time) VALUES (?, ?, ?, ?)", roomID, msg.Sender, msg.Text, msg.Time)
		json.NewEncoder(w).Encode(msg)
	} else {
		rows, _ := db.Query("SELECT id, sender, text, time FROM messages WHERE room_id = ? ORDER BY time ASC", roomID)
		defer rows.Close()
		var messages []Message
		for rows.Next() {
			var msg Message
			rows.Scan(&msg.ID, &msg.Sender, &msg.Text, &msg.Time)
			messages = append(messages, msg)
		}
		json.NewEncoder(w).Encode(messages)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading to websocket: %v", err)
		return
	}
	defer ws.Close()

	clients[ws] = true

	for {
		var msg Message
		err := ws.ReadJSON(&msg)
		if err != nil {
			log.Printf("Error reading json: %v", err)
			delete(clients, ws)
			break
		}

		msg.Time = time.Now()
		// Broadcast ข้อความไปยังทุกคนที่เชื่อมต่อ
		broadcast <- msg

		// บันทึกข้อความในฐานข้อมูล
		mu.Lock()
		db.Exec("INSERT INTO messages (room_id, sender, text, time) VALUES (?, ?, ?, ?)", msg.RoomID, msg.Sender, msg.Text, msg.Time)
		mu.Unlock()
	}
}

func handleMessages() {
	for {
		msg := <-broadcast
		for client := range clients {
			err := client.WriteJSON(msg)
			if err != nil {
				log.Printf("Error writing json: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}