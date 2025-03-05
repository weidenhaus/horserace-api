package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

// Horse represents a racing horse
type Horse struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Position float64 `json:"position"`
	Speed    float64 `json:"speed"`
	Color    string  `json:"color"`
}

// Event represents a message sent to clients
type Event struct {
	Type           string  `json:"type"`
	Message        string  `json:"message,omitempty"`
	Horses         []Horse `json:"horses,omitempty"`
	Winner         *Horse  `json:"winner,omitempty"`
	RaceInProgress bool    `json:"raceInProgress,omitempty"`
	RaceLength     int     `json:"raceLength,omitempty"`
}

// Server represents the race server
type Server struct {
	clients        map[chan []byte]bool
	register       chan chan []byte
	unregister     chan chan []byte
	broadcast      chan []byte
	horses         []Horse
	raceInProgress bool
	raceLength     int
	mu             sync.Mutex
}

func NewServer() *Server {
	return &Server{
		clients:        make(map[chan []byte]bool),
		register:       make(chan chan []byte),
		unregister:     make(chan chan []byte),
		broadcast:      make(chan []byte, 100), // Buffered channel to prevent blocking
		raceInProgress: false,
		raceLength:     1000,
		horses: []Horse{
			{ID: 1, Name: "Thunderbolt", Position: 0, Speed: 0, Color: "#E74C3C"},
			{ID: 2, Name: "Silver Arrow", Position: 0, Speed: 0, Color: "#3498DB"},
			{ID: 3, Name: "Golden Hoof", Position: 0, Speed: 0, Color: "#F1C40F"},
			{ID: 4, Name: "Midnight Star", Position: 0, Speed: 0, Color: "#2C3E50"},
			{ID: 5, Name: "Lucky Charm", Position: 0, Speed: 0, Color: "#27AE60"},
		},
	}
}

func (s *Server) Run() {
	go func() {
		for {
			select {
			case client := <-s.register:
				s.clients[client] = true
				log.Println("New client registered, total clients:", len(s.clients))
				s.sendInitialState(client)
			case client := <-s.unregister:
				if _, ok := s.clients[client]; ok {
					delete(s.clients, client)
					close(client)
					log.Println("Client unregistered, total clients:", len(s.clients))
				}
			case message := <-s.broadcast:
				for client := range s.clients {
					select {
					case client <- message:
						// Message sent successfully
					default:
						// Client channel is blocked, remove it
						delete(s.clients, client)
						close(client)
						log.Println("Client removed due to blocking, total clients:", len(s.clients))
					}
				}
			}
		}
	}()
}

func (s *Server) sendInitialState(client chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	initialEvent := Event{
		Type:           "init",
		Horses:         s.horses,
		RaceInProgress: s.raceInProgress,
		RaceLength:     s.raceLength,
	}

	eventJSON, err := json.Marshal(initialEvent)
	if err != nil {
		log.Println("Error marshaling initial state:", err)
		return
	}

	client <- formatSSE(eventJSON)
}

func (s *Server) broadcastEvent(event Event) {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		log.Println("Error marshaling event:", err)
		return
	}

	s.broadcast <- formatSSE(eventJSON)
}

func formatSSE(data []byte) []byte {
	return []byte(fmt.Sprintf("data: %s\n\n", data))
}

// CORS middleware
func EnableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SSE event handler with improved connection handling
func (s *Server) HandleEvents(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Important: disable any response buffering
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	} else {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create a buffered channel for this client to prevent blocking
	clientChan := make(chan []byte, 100)

	// Register this client
	s.register <- clientChan

	// Keep the client alive with a ping message every 15 seconds
	// This prevents proxy servers from closing the connection
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Send a comment line as a keepalive ping
				select {
				case clientChan <- []byte(": ping\n\n"):
					// Ping sent
				default:
					// Channel is blocked, client might be gone
				}
			}
		}
	}()

	// Clean up when done
	defer func() {
		s.unregister <- clientChan
		log.Println("Client handler completed")
	}()

	// Keep the connection open and stream events
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-clientChan:
			if !ok {
				return
			}

			_, err := w.Write(msg)
			if err != nil {
				log.Println("Error writing to client:", err)
				return
			}

			// Critical: flush the data immediately to the client
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// Start race endpoint
func (s *Server) StartRace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	if s.raceInProgress {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Race already in progress"})
		return
	}

	s.raceInProgress = true

	// Reset horses
	for i := range s.horses {
		s.horses[i].Position = 0
		s.horses[i].Speed = 0
	}
	s.mu.Unlock()

	// Broadcast race start
	s.broadcastEvent(Event{
		Type:    "raceStart",
		Message: "And they're off!",
		Horses:  s.horses,
	})

	// Start race simulation
	go s.simulateRace()

	// Return success response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Race simulation
func (s *Server) simulateRace() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var raceFinished bool
	var finishedHorses []Horse

	for range ticker.C {
		s.mu.Lock()

		if !s.raceInProgress {
			s.mu.Unlock()
			return
		}

		// Find current lead horse for commentary
		var leadHorse Horse
		maxPosition := 0.0

		// Update all horses
		for i := range s.horses {
			// Randomize speed changes
			speedChange := rand.Float64()*2 - 0.5
			newSpeed := s.horses[i].Speed + speedChange
			newSpeed = max(1.0, min(10.0, newSpeed))
			newSpeed = float64(int(newSpeed*10)) / 10

			newPosition := s.horses[i].Position + newSpeed
			newPosition = float64(int(newPosition*10)) / 10
			s.horses[i].Speed = newSpeed
			s.horses[i].Position = min(float64(s.raceLength), newPosition)

			// Track lead horse
			if s.horses[i].Position > maxPosition {
				maxPosition = s.horses[i].Position
				leadHorse = s.horses[i]
			}

			// Check for finished horses
			if s.horses[i].Position >= float64(s.raceLength) && !raceFinished {
				finishedHorses = append(finishedHorses, s.horses[i])
			}
		}

		// Send position update
		s.broadcastEvent(Event{
			Type:   "raceUpdate",
			Horses: s.horses,
		})

		// Add commentary occasionally
		if rand.Float64() < 0.1 {
			s.broadcastEvent(Event{
				Type:    "commentary",
				Message: s.generateCommentary(leadHorse),
			})
		}

		// Check if race is finished
		if len(finishedHorses) > 0 && !raceFinished {
			raceFinished = true
			winner := finishedHorses[0]

			// Unlock before ending race
			s.mu.Unlock()

			// Small delay before announcing winner
			time.Sleep(1 * time.Second)

			// End the race with the winner
			s.endRace(winner)
			return
		}

		s.mu.Unlock()
	}
}

func (s *Server) endRace(winner Horse) {
	s.mu.Lock()
	s.raceInProgress = false
	s.mu.Unlock()

	// Announce the winner
	s.broadcastEvent(Event{
		Type:    "raceEnd",
		Winner:  &winner,
		Message: fmt.Sprintf("%s has won the race!", winner.Name),
	})
}

func (s *Server) generateCommentary(leadHorse Horse) string {
	commentaries := []string{
		fmt.Sprintf("%s is taking the lead!", leadHorse.Name),
		fmt.Sprintf("Look at %s go!", leadHorse.Name),
		fmt.Sprintf("%s is pulling ahead of the pack!", leadHorse.Name),
		fmt.Sprintf("It's a close race between %s and %s!", s.horses[0].Name, s.horses[1].Name),
		fmt.Sprintf("%s is making a move!", s.horses[rand.Intn(len(s.horses))].Name),
	}

	return commentaries[rand.Intn(len(commentaries))]
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func main() {
	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	// Create and start the server
	server := NewServer()
	server.Run()

	// Configure routes
	mux := http.NewServeMux()

	// Add handlers with CORS
	mux.Handle("/events", EnableCORS(http.HandlerFunc(server.HandleEvents)))
	mux.Handle("/start-race", EnableCORS(http.HandlerFunc(server.StartRace)))

	// Add a health check endpoint
	mux.Handle("/health", EnableCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})))

	// Root handler
	mux.Handle("/", EnableCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Horse Race Server is running. Use /events for SSE and /start-race to begin a race.")
	})))

	port := os.Getenv("PORT")
	if port == "" {
		port = "5555"
	}
	// Configure the server with longer timeout
	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // Long write timeout for SSE
		IdleTimeout:  120 * time.Second,
	}

	// Start HTTP server
	fmt.Println("Horse race server running on port " + port)
	log.Fatal(httpServer.ListenAndServe())
}
