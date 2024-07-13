package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

// Declare global variables
var (
	db                 *sql.DB
	listOfVotingSystem []string
)

// struct for application
type application struct {
	infoLog *log.Logger
	errLog  *log.Logger
	DB      *sql.DB
}

// struct for response body
type Response struct {
	Message string `json:"message"`
}

// struct for results respose body
type Result struct {
	Results map[string]int `json:"results"`
}

// struct for casting vote
type Vote struct {
	VoteID string `json:"vote_id"`
	Email  string `json:"email"`
	Option string `json:"option"`
}

// struct for creating vote record
type Voter struct {
	VoterID  string   `json:"voter_id"`
	Options  []string `json:"options"`
	UserList []string `json:"user_list"`
}

// init function to initialize database and get existing vote_id into global variable
func init() {
	log.Println("init function start")
	err := godotenv.Load()
	if err != nil {
		log.Fatal("init: Error loading .env file", err)
	}
	log.Println("init: Loaded .env file")

	// Connect to DB and test the connection
	connectString := fmt.Sprintf("%s:%s@/%s?parseTime=true", os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME"))

	log.Println("init: Connecting to database")
	// here db must not be block scoped(created as new like db :=), it must be from declared global variable(db =)
	db, err = sql.Open("mysql", connectString)
	if err != nil {
		log.Fatal(err.Error())
	}
	// defer db.Close()
	if err = db.Ping(); err != nil {
		log.Fatal(err.Error())
		return
	} else {
		log.Println("init: db connected")
	}
	log.Println("init: Populating listOfVotingSystem with existing voting systems")
	query := `select voter_id from votersystem`
	rows, err := db.Query(query)
	if err != nil {
		log.Printf("init: error querying for voter_ids: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var voter_id string
		err := rows.Scan(&voter_id)

		if err != nil {
			log.Println("init: Error scanning row:", err)
			continue
		}

		listOfVotingSystem = append(listOfVotingSystem, voter_id)
	}

	if err := rows.Err(); err != nil {
		log.Printf("init: error iterating rows: %v\n", err)
	}
	log.Println("init: existing voting systems before application starts: ", strings.Join(listOfVotingSystem, ", "))
	log.Println("init: closed")
}

// remove duplicate from inputs
func (app *application) removeDuplicates(list []string) []string {
	seen := make(map[string]bool)
	var result []string
	app.infoLog.Println("Given list:", list)
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	app.infoLog.Println("list after removing dupicates:", result)
	return result
}

// Define routes
func (app *application) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/create_vote", app.createVoterRecord)
	mux.HandleFunc("/vote", app.castVote)
	mux.HandleFunc("/vote_result", app.getResult)
	mux.HandleFunc("/delete_all_voters", app.deleteAllVotingSystems)
	return mux
}

// function to create voter record in DB and then send mail notification to user
func (app *application) createVoterRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// define response body
	response := Response{
		Message: "Vote created and emails sent",
	}
	responseJSON, _ := json.Marshal(response)

	var voter Voter

	if err := json.NewDecoder(r.Body).Decode(&voter); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// fmt.Println("voter.Options: ", voter.Options)
	// fmt.Println("voter.UserList: ", voter.UserList)
	// fmt.Println("voter.UserList: ", voter.VoterID)
	optionsJSON, err := json.Marshal(app.removeDuplicates(voter.Options))
	if err != nil {
		http.Error(w, "Error processing options", http.StatusInternalServerError)
		return
	}
	// fmt.Println("optionsJSON:", optionsJSON)
	finalUsersList := app.removeDuplicates(voter.UserList)
	userListJSON, err := json.Marshal(finalUsersList)
	if err != nil {
		http.Error(w, "Error processing user list", http.StatusInternalServerError)
		return
	}
	// fmt.Println("userListJSON: ", userListJSON)
	stmt := `insert into votersystem (voter_id, options, user_list, created_at) VALUES(?, ?, ?, UTC_TIMESTAMP());`

	// fmt.Println(voter)
	_, err = app.DB.Exec(stmt, voter.VoterID, optionsJSON, userListJSON)
	// _, err := app.DB.Exec(stmt, voter.VoterID, voter.Options, voter.UserList)
	if err != nil {
		app.errLog.Println(err.Error())
		http.Error(w, "Failed to insert data", http.StatusInternalServerError)
		return
	}
	stmt = fmt.Sprintf("create table if not exists contx_%s (user_id VARCHAR(255) PRIMARY KEY, optionchoosen VARCHAR(255), remindersent BOOL DEFAULT FALSE, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);", voter.VoterID)
	// fmt.Println(stmt)
	_, err = app.DB.Exec(stmt)
	if err != nil {
		app.errLog.Println(err.Error())
		http.Error(w, "Failed to create vote recoding table", http.StatusInternalServerError)
		return
	}
	listOfVotingSystem = append(listOfVotingSystem, voter.VoterID)
	app.infoLog.Printf("table contx_%s is created", voter.VoterID)
	stmt = fmt.Sprintf(`insert into contx_%s (user_id) VALUES(?);`, voter.VoterID)
	// fmt.Println(stmt)
	app.infoLog.Println("Final usersList is:")
	for _, email := range finalUsersList {
		app.infoLog.Println(email)
		go app.sendNotification(email, voter.VoterID, voter.Options)
		_, err = app.DB.Exec(stmt, email)
		if err != nil {
			app.errLog.Println(err.Error())
			http.Error(w, fmt.Sprintf("Failed to insert user:%s record in contx_%s", email, voter.VoterID), http.StatusInternalServerError)
			// return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(responseJSON)

}

// delete existing rows
func (app *application) deleteAllVotingSystems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	for _, table := range listOfVotingSystem {
		go func(table string) {
			stmt := fmt.Sprintf("drop table contx_%s;", table)
			fmt.Println(stmt)
			_, err := app.DB.Exec(stmt)
			if err != nil {
				log.Println(err.Error())
			}
		}(table)
	}
	listOfVotingSystem = []string{}

	stmt := "DELETE FROM votersystem"
	result, err := app.DB.Exec(stmt)
	if err != nil {
		app.errLog.Println("Error deleting data:", err.Error())
		http.Error(w, "Failed to delete data", http.StatusInternalServerError)
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		app.errLog.Println("Error getting rows affected:", err.Error())
		http.Error(w, "Failed to delete data", http.StatusInternalServerError)
		return
	}

	response := fmt.Sprintf("Deleted %d rows from voters table", rowsAffected)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))

}

// cast vote
func (app *application) castVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var response *Response
	var voter Vote
	err := json.NewDecoder(r.Body).Decode(&voter)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	var voteCheck sql.NullString
	query := fmt.Sprintf("select optionchoosen from contx_%s WHERE user_id = ?", voter.VoteID)
	err = app.DB.QueryRow(query, voter.Email).Scan(&voteCheck)
	if err != nil {
		if err == sql.ErrNoRows {
			response = &Response{
				Message: fmt.Sprintf("No record found for %s", voter),
			}
			w.WriteHeader(http.StatusNotFound)
			app.errLog.Printf("No record found for %s", voter)

		} else {
			response = &Response{
				Message: fmt.Sprintf("Error querying option field: %v", err),
			}
			w.WriteHeader(http.StatusInternalServerError)
			app.errLog.Printf("Error querying option field: %v", err)
		}
		responseJSON, _ := json.Marshal(response)
		// w.WriteHeader(http.StatusInternalServerError)
		w.Write(responseJSON)
		return
	}
	// fmt.Println(voteCheck)
	if voteCheck.Valid {
		response = &Response{
			Message: "Your vote is already recorded, you can't vote again",
		}
	} else {
		query = fmt.Sprintf("UPDATE contx_%s SET optionchoosen = ? WHERE user_id = ?", voter.VoteID)
		// app.infoLog.Println(query)
		fmt.Println(voter.VoteID, voter.Email, voter.Option)
		_, err = app.DB.Exec(query, voter.Option, voter.Email)
		if err != nil {
			http.Error(w, "Failed to update vote status", http.StatusInternalServerError)
			return
		}
		response = &Response{
			Message: "Your vote is recorded",
		}
	}
	responseJSON, _ := json.Marshal(response)

	w.WriteHeader(http.StatusOK)
	w.Write(responseJSON)
}

// sent notification
func (app *application) sendNotification(recipient string, vote_id string, options []string) {
	auth := smtp.PlainAuth(
		"",
		"krishnaps909@gmail.com",
		os.Getenv("EMAIL_PASSWORD"), // Use an App Password if 2FA is enabled
		"smtp.gmail.com",
	)

	to := []string{recipient}
	var msg []byte
	if len(options) < 1 {
		msg = []byte(fmt.Sprintf(
			"To: %s\r\n"+
				"Subject: Reminder to vote\r\n"+
				"\r\n"+
				"This is reminder to vote for the voting system: %s that you been nominated.\r\n"+
				"Please follow the instructions sent in another email with \bSubject: Added to voting system\b. \r\n", recipient, vote_id))
	} else {
		msg = []byte(fmt.Sprintf(
			"To: %s\r\n"+
				"Subject: Added to voting system\r\n"+
				"\r\n"+
				"You have been added to the voting system with the following details:\r\n"+
				"\r\n"+
				"Vote ID: %s\r\n"+
				"Options: %v\r\n"+
				"\r\n"+
				"Please cast your vote using the following URL:\r\n"+
				"http://localhost:4000/vote\r\n"+
				"\r\n"+
				"Example request:\r\n"+
				"curl -X POST http://localhost:4000/vote -H \"Content-Type: application/json\" -d '{\"vote_id\": \"%s\", \"email\": \"%s\", \"option\": \"Option1\"}'\r\n",
			recipient, vote_id, options, vote_id, recipient))

	}
	err := smtp.SendMail(
		"smtp.gmail.com:587",
		auth,
		"krishnaps909@gmail.com",
		to,
		msg,
	)

	if err != nil {
		app.errLog.Println(err.Error())
	}
	app.infoLog.Printf("Email sent successfully to %s\n", recipient)
}

// sent reminder
func (app *application) sendReminder(table string) {
	// Query emails where castvote is false

	app.infoLog.Println("Checking for user voting status in", table)
	query := fmt.Sprintf("select user_id from contx_%s where optionchoosen IS NULL AND remindersent = 0 AND TIMESTAMPDIFF(MINUTE, created_at, NOW()) > 1;", table)
	rows, err := app.DB.Query(query)
	if err != nil {
		app.errLog.Printf("error querying emails: %v", err)
	}
	defer rows.Close()

	// fmt.Println(rows)
	for rows.Next() {
		var email string
		// var voterid string
		err := rows.Scan(&email)
		if err != nil {
			app.errLog.Println("Error scanning row:", err)
			continue
		}
		go app.sendNotification(email, table, []string{})
		query := fmt.Sprintf("UPDATE contx_%s SET remindersent = 1 WHERE user_id = ?", table)
		_, err = app.DB.Exec(query, email)
		if err != nil {
			app.errLog.Printf("can't update the remindersent value =: %v", err)
		}
	}

	if err := rows.Err(); err != nil {
		app.errLog.Printf("error iterating rows: %v\n", err)
	}

}

// check if user has voter
func (app *application) hasUserCastVote() {
	for {
		// app.infoLog.Println("checking for voting status")
		for _, table := range listOfVotingSystem {
			go app.sendReminder(table)
		}
		time.Sleep(time.Second * 10)
	}
}

func (app *application) getResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	voteID := r.URL.Query().Get("vote_id")
	if voteID == "" {
		http.Error(w, "Missing vote_id parameter", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("SELECT optionchoosen, COUNT(*) as count FROM contx_%s GROUP BY optionchoosen", voteID)
	rows, err := app.DB.Query(query)
	if err != nil {
		http.Error(w, "Database query error", http.StatusInternalServerError)
		return
	}
	go func(voteID string) {
		result := []string{}
		for _, v := range listOfVotingSystem {
			if v != voteID {
				result = append(result, v)
			}
		}
		listOfVotingSystem = result

	}(voteID)
	defer rows.Close()

	results := make(map[string]int)
	for rows.Next() {
		var option string
		var count int
		if err := rows.Scan(&option, &count); err != nil {
			http.Error(w, "Error scanning row", http.StatusInternalServerError)
			return
		}
		results[option] = count
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "Error iterating over rows", http.StatusInternalServerError)
		return
	}

	result := Result{Results: results}
	jsonResponse, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonResponse)
}

func main() {
	// get the port details
	addr := flag.String("addr", ":4000", "network port for connection")
	flag.Parse()

	// set custom logs
	infoLog := log.New(os.Stdout, "INFO:\t", log.Ldate|log.Ltime)
	errLog := log.New(os.Stderr, "Error:\t", log.LUTC|log.Llongfile)
	infoLog.Println("main function started")
	// // load secrets from .env
	// err := godotenv.Load()
	// if err != nil {
	// 	errLog.Fatal("Error loading .env file", err)
	// }
	// // Connect to DB and test the connection
	// connectString := fmt.Sprintf("%s:%s@/%s?parseTime=true", os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME"))
	// db, err := sql.Open("mysql", connectString)
	// if err != nil {
	// 	errLog.Fatal(err.Error())
	// }
	defer db.Close()
	if err := db.Ping(); err != nil {
		errLog.Fatal(err.Error())
		return
	} else {
		infoLog.Println("db connectivity tested successfully")
	}
	// initialise application struct
	app := &application{infoLog: infoLog, errLog: errLog, DB: db}

	infoLog.Printf("Starting server on port :%v", *addr)

	go app.hasUserCastVote() //Call function that triggers go routine for notifications and reminders

	// start the web server with custom parameters
	srv := &http.Server{
		Handler:  app.routes(),
		ErrorLog: errLog,
		Addr:     *addr,
	}
	err := srv.ListenAndServe()
	errLog.Println(err.Error())
}
