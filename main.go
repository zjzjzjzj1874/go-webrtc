package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ClientInfo struct {
	Conn       *websocket.Conn
	IP         string
	UserAgent  string
	JoinTime   time.Time
	LastActive time.Time
}

type Room struct {
	ID       string
	Password string
	Host     *websocket.Conn
	Clients  map[*websocket.Conn]*ClientInfo
	Mutex    sync.RWMutex
}

type Message struct {
	Type     string          `json:"type"`
	Room     string          `json:"room,omitempty"`
	Password string          `json:"password,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	IsQuick  bool            `json:"is_quick,omitempty"` // 是否是快速会议
}

var (
	rooms     = make(map[string]*Room)
	roomMutex sync.RWMutex
	upgrader  = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func printOnlineUsers() {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	log.Println("========== 当前在线用户统计 ===========")
	for roomID, room := range rooms {
		room.Mutex.RLock()
		log.Printf("房间 %s: %d 个用户", roomID, len(room.Clients))
		for _, client := range room.Clients {
			log.Printf("  - IP: %s, 设备: %s, 在线时长: %v",
				client.IP,
				client.UserAgent,
				time.Since(client.JoinTime).Round(time.Second))
		}
		room.Mutex.RUnlock()
	}
	log.Println("=====================================")
}

func main() {
	// 设置路由
	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("static")))

	// 启动定时统计任务
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			printOnlineUsers()
			cleanInactiveUsers()
		}
	}()

	// 启动HTTPS服务器
	port := ":54321"
	fmt.Printf("Server is running on port %s\n", port)
	log.Fatal(http.ListenAndServeTLS(port, "cert.pem", "key.pem", nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	log.Printf("新的WebSocket连接请求: %s, 设备: %s", r.RemoteAddr, r.UserAgent())
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}
	defer func() {
		log.Printf("WebSocket连接关闭: %s", conn.RemoteAddr())
		conn.Close()
	}()

	for {
		// 读取消息
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			break
		}

		switch msg.Type {
		case "create":
			if msg.IsQuick {
				handleQuickMeeting(conn, &msg, r)
			} else {
				handleCreateRoom(conn, &msg, r)
			}
		case "join":
			handleJoinRoom(conn, &msg, r)
		case "ice-candidate", "offer", "answer":
			handleWebRTCMessage(conn, &msg)
		case "heartbeat":
			handleHeartbeat(conn, &msg)
		}
	}
}

func cleanInactiveUsers() {
	roomMutex.Lock()
	defer roomMutex.Unlock()

	timeoutDuration := 1 * time.Minute
	now := time.Now()

	for roomID, room := range rooms {
		room.Mutex.Lock()
		for conn, client := range room.Clients {
			if now.Sub(client.LastActive) > timeoutDuration {
				log.Printf("清理超时用户: %s, 房间: %s", client.IP, roomID)
				delete(room.Clients, conn)
				conn.Close()
			}
		}
		// 如果房间没有用户了，删除房间
		if len(room.Clients) == 0 {
			log.Printf("删除空房间: %s", roomID)
			delete(rooms, roomID)
		}
		room.Mutex.Unlock()
	}
}

func handleHeartbeat(conn *websocket.Conn, msg *Message) {
	roomMutex.RLock()
	defer roomMutex.RUnlock()

	for _, room := range rooms {
		room.Mutex.Lock()
		if client, exists := room.Clients[conn]; exists {
			client.LastActive = time.Now()
			conn.WriteJSON(Message{Type: "heartbeat-ack"})
		}
		room.Mutex.Unlock()
	}
}

func handleQuickMeeting(conn *websocket.Conn, msg *Message, r *http.Request) {
	log.Printf("尝试创建快速会议: %s", msg.Room)
	roomMutex.Lock()
	defer roomMutex.Unlock()

	if _, exists := rooms[msg.Room]; exists {
		log.Printf("房间已存在: %s", msg.Room)
		conn.WriteJSON(Message{Type: "error", Payload: []byte(`"Room already exists"`)})
		return
	}

	rooms[msg.Room] = &Room{
		ID:       msg.Room,
		Password: msg.Password,
		Host:     conn,
		Clients:  make(map[*websocket.Conn]*ClientInfo),
	}
	rooms[msg.Room].Clients[conn] = &ClientInfo{
		Conn:       conn,
		IP:         conn.RemoteAddr().String(),
		UserAgent:  r.UserAgent(),
		JoinTime:   time.Now(),
		LastActive: time.Now(),
	}

	conn.WriteJSON(Message{Type: "created", Room: msg.Room, IsQuick: true})
}

func handleCreateRoom(conn *websocket.Conn, msg *Message, r *http.Request) {
	log.Printf("尝试创建普通会议房间: %s", msg.Room)
	roomMutex.Lock()
	defer roomMutex.Unlock()

	if _, exists := rooms[msg.Room]; exists {
		log.Printf("房间已存在: %s", msg.Room)
		conn.WriteJSON(Message{Type: "error", Payload: []byte(`"Room already exists"`)})
		return
	}

	if msg.Password == "" {
		conn.WriteJSON(Message{Type: "error", Payload: []byte(`"Password is required for regular meeting room"`)})
		return
	}

	rooms[msg.Room] = &Room{
		ID:       msg.Room,
		Password: msg.Password,
		Host:     conn,
		Clients:  make(map[*websocket.Conn]*ClientInfo),
	}
	rooms[msg.Room].Clients[conn] = &ClientInfo{
		Conn:       conn,
		IP:         conn.RemoteAddr().String(),
		UserAgent:  r.UserAgent(),
		JoinTime:   time.Now(),
		LastActive: time.Now(),
	}

	conn.WriteJSON(Message{Type: "created", Room: msg.Room})
}

func handleJoinRoom(conn *websocket.Conn, msg *Message, r *http.Request) {
	log.Printf("尝试加入房间: %s", msg.Room)
	roomMutex.RLock()
	room, exists := rooms[msg.Room]
	roomMutex.RUnlock()

	if !exists {
		log.Printf("房间不存在: %s", msg.Room)
		conn.WriteJSON(Message{Type: "error", Payload: []byte(`"Room not found"`)})
		return
	}

	if !msg.IsQuick && room.Password != msg.Password {
		log.Printf("密码错误: %s", msg.Room)
		conn.WriteJSON(Message{Type: "error", Payload: []byte(`"Invalid password"`)})
		return
	}

	room.Mutex.Lock()
	room.Clients[conn] = &ClientInfo{
		Conn:       conn,
		IP:         conn.RemoteAddr().String(),
		UserAgent:  r.UserAgent(),
		JoinTime:   time.Now(),
		LastActive: time.Now(),
	}
	room.Mutex.Unlock()

	// 通知房间内其他成员有新用户加入，并请求他们发送offer
	for client := range room.Clients {
		if client != conn {
			client.WriteJSON(Message{Type: "user-joined", Room: msg.Room})
			client.WriteJSON(Message{Type: "request-offer", Room: msg.Room})
		}
	}

	conn.WriteJSON(Message{Type: "joined", Room: msg.Room})
}

func handleWebRTCMessage(conn *websocket.Conn, msg *Message) {
	log.Printf("收到WebRTC消息: 类型=%s, 房间=%s", msg.Type, msg.Room)
	roomMutex.RLock()
	room, exists := rooms[msg.Room]
	roomMutex.RUnlock()

	if !exists {
		log.Printf("房间不存在，无法处理WebRTC消息: %s", msg.Room)
		return
	}

	room.Mutex.RLock()
	defer room.Mutex.RUnlock()

	// 广播WebRTC消息给房间内其他成员
	for client := range room.Clients {
		if client != conn {
			client.WriteJSON(msg)
		}
	}
}
