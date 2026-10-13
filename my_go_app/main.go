package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/sessions"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

var (
	db    *sql.DB
	store = sessions.NewCookieStore([]byte("super-secret-key-change-this"))
	tmpl  *template.Template
)

type User struct {
	ID       int
	Username string
	Role     string
}

type Order struct {
	ID       int
	UserID   int
	GameName string
	PlayerID string
	Amount   float64
	Status   string
	Username string
}

func init() {
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600 * 2,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func main() {
	var err error

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgresql://myuser:Hz03Ci4rkBWsCJEZyeF49fELOdtCJSiu@dpg-db77honavr4c73a03kpg-a.singapore-postgres.render.com/mydb_ott6?sslmode=require"
	}

	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal("Connection failed:", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatal("Ping DB failed:", err)
	}
	fmt.Println("Connected to Render PostgreSQL successfully!")

	// 1. สั่งสร้างตารางถ้ายังไม่มี
	createTablesSQL := `
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		username VARCHAR(50) UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role VARCHAR(20) NOT NULL DEFAULT 'user',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS topup_orders (
		id SERIAL PRIMARY KEY,
		user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		game_name VARCHAR(100) NOT NULL,
		player_id VARCHAR(100) NOT NULL,
		amount DECIMAL(10, 2) NOT NULL,
		status VARCHAR(50) DEFAULT 'Pending',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

	_, err = db.Exec(createTablesSQL)
	if err != nil {
		log.Fatal("Failed to create tables:", err)
	}

	// 2. ปิดคำสั่ง TRUNCATE เพื่อไม่ให้ข้อมูลผู้ใช้และออเดอร์ถูกลบเมื่อรีสตาร์ท Server บน Production
	// _, err = db.Exec("TRUNCATE TABLE topup_orders, users RESTART IDENTITY CASCADE;")

	// 3. สร้างไอดีทดสอบชุดใหม่ (admin, user1 / รหัส 123456)
	seedAccounts()

	// 4. เช็กรายชื่อผู้ใช้ใน DB
	checkDatabaseUsers()

	// โหลดไฟล์ HTML ในโฟลเดอร์ templates ทั้งหมด
	tmpl = template.Must(template.ParseGlob("templates/*.html"))

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	fileServer := http.FileServer(http.Dir("./static"))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))

	// Routes สาธารณะ
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	r.Get("/register", registerPage)
	r.Post("/register", registerHandler)
	r.Get("/login", loginPage)
	r.Post("/login", loginHandler)
	r.Get("/logout", logoutHandler)

	// Routes สิทธิ์ User
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware)

		r.Get("/dashboard", dashboardPage)   // หน้าเติมเกม UID
		r.Get("/cards", gameCardsPage)       // หน้าบัตรเติมเกม
		r.Get("/history", orderHistoryPage)  // หน้าประวัติสั่งซื้อ
		r.Get("/privacy", privacyPolicyPage) // หน้านโยบาย Privacy
		r.Get("/refund", refundPolicyPage)   // หน้านโยบาย Refund

		r.Post("/orders/create", createOrderHandler)
		r.Get("/orders/edit/{id}", editOrderPage)
		r.Post("/orders/edit/{id}", updateOrderHandler)
		r.Post("/orders/delete/{id}", deleteOrderHandler)
	})

	// Routes สิทธิ์ Admin
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware)
		r.Use(AdminMiddleware)

		r.Get("/admin", adminPage)
		r.Post("/admin/orders/status/{id}", updateOrderStatusHandler)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("\n🚀 Server running on http://localhost:%s ...\n", port)
	http.ListenAndServe(":"+port, r)
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	err := tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template Render Error (%s): %v", name, err), http.StatusInternalServerError)
	}
}

func checkDatabaseUsers() {
	rows, err := db.Query("SELECT id, username, role FROM users")
	if err != nil {
		fmt.Println("Error reading users:", err)
		return
	}
	defer rows.Close()

	fmt.Println("-------------------------------------------")
	fmt.Println("📊 รายชื่อผู้ใช้ที่มีอยู่ใน Database ปัจจุบัน:")
	for rows.Next() {
		var id int
		var username, role string
		rows.Scan(&id, &username, &role)
		fmt.Printf(" - ID: %d | Username: %s | Role: %s\n", id, username, role)
	}
	fmt.Println("-------------------------------------------")
}

// --- Middlewares & Handlers ---

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, "session-name")
		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, "session-name")
		role, _ := session.Values["role"].(string)
		if role != "admin" {
			http.Error(w, "403 Forbidden: Admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func registerPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "register.html", nil)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	role := "user"
	if count == 0 {
		role = "admin"
	}

	_, err = db.Exec("INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3)", username, string(hashedPassword), role)
	if err != nil {
		http.Error(w, "Username already exists or DB error", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func loginPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "login.html", nil)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	var id int
	var hash, role string
	err := db.QueryRow("SELECT id, password_hash, role FROM users WHERE username = $1", username).Scan(&id, &hash, &role)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	session, _ := store.Get(r, "session-name")
	session.Values["authenticated"] = true
	session.Values["user_id"] = id
	session.Values["username"] = username
	session.Values["role"] = role
	session.Save(r, w)

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func dashboardPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	data := map[string]interface{}{
		"Username": session.Values["username"],
		"Role":     session.Values["role"],
	}
	renderTemplate(w, "dashboard.html", data)
}

func gameCardsPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	data := map[string]interface{}{
		"Username": session.Values["username"],
		"Role":     session.Values["role"],
	}
	renderTemplate(w, "game_cards.html", data)
}

func orderHistoryPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	userID, ok := session.Values["user_id"].(int)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	rows, err := db.Query("SELECT id, game_name, player_id, amount, status FROM topup_orders WHERE user_id = $1 ORDER BY id DESC", userID)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		rows.Scan(&o.ID, &o.GameName, &o.PlayerID, &o.Amount, &o.Status)
		orders = append(orders, o)
	}

	data := map[string]interface{}{
		"Username": session.Values["username"],
		"Role":     session.Values["role"],
		"Orders":   orders,
	}
	renderTemplate(w, "order_history.html", data)
}

func privacyPolicyPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	data := map[string]interface{}{
		"Username": session.Values["username"],
		"Role":     session.Values["role"],
	}
	renderTemplate(w, "privacy_policy.html", data)
}

func refundPolicyPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	data := map[string]interface{}{
		"Username": session.Values["username"],
		"Role":     session.Values["role"],
	}
	renderTemplate(w, "refund_policy.html", data)
}

func createOrderHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	userID, ok := session.Values["user_id"].(int)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	gameName := r.FormValue("game_name")
	playerID := r.FormValue("player_id")
	amount := r.FormValue("amount")

	db.Exec("INSERT INTO topup_orders (user_id, game_name, player_id, amount) VALUES ($1, $2, $3, $4)", userID, gameName, playerID, amount)
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func editOrderPage(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	userID, ok := session.Values["user_id"].(int)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	orderID := chi.URLParam(r, "id")

	var o Order
	err := db.QueryRow("SELECT id, game_name, player_id, amount FROM topup_orders WHERE id = $1 AND user_id = $2", orderID, userID).Scan(&o.ID, &o.GameName, &o.PlayerID, &o.Amount)
	if err != nil {
		http.Error(w, "Order not found or unauthorized", http.StatusForbidden)
		return
	}

	renderTemplate(w, "edit_order.html", o)
}

func updateOrderHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	userID, ok := session.Values["user_id"].(int)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	orderID := chi.URLParam(r, "id")

	playerID := r.FormValue("player_id")

	res, err := db.Exec("UPDATE topup_orders SET player_id = $1 WHERE id = $2 AND user_id = $3", playerID, orderID, userID)
	if err != nil {
		http.Error(w, "DB Error", http.StatusInternalServerError)
		return
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		http.Error(w, "Unauthorized", http.StatusForbidden)
		return
	}

	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func deleteOrderHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	userID, ok := session.Values["user_id"].(int)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	orderID := chi.URLParam(r, "id")

	db.Exec("DELETE FROM topup_orders WHERE id = $1 AND user_id = $2", orderID, userID)
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func adminPage(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT topup_orders.id, topup_orders.game_name, topup_orders.player_id, topup_orders.amount, topup_orders.status, users.username FROM topup_orders JOIN users ON topup_orders.user_id = users.id ORDER BY topup_orders.id DESC")
	if err != nil {
		http.Error(w, "DB Error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var allOrders []Order
	for rows.Next() {
		var o Order
		rows.Scan(&o.ID, &o.GameName, &o.PlayerID, &o.Amount, &o.Status, &o.Username)
		allOrders = append(allOrders, o)
	}

	renderTemplate(w, "admin.html", allOrders)
}

func seedAccounts() {
	createDefaultUser("admin", "123456", "admin")
	createDefaultUser("user1", "123456", "user")
}

func createDefaultUser(username, password, role string) {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE username = $1", username).Scan(&count)

	if count == 0 {
		hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		db.Exec("INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3)", username, string(hashedPassword), role)
	}
}

func updateOrderStatusHandler(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "id")
	newStatus := r.FormValue("status")

	_, err := db.Exec("UPDATE topup_orders SET status = $1 WHERE id = $2", newStatus, orderID)
	if err != nil {
		http.Error(w, "Failed to update status", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
