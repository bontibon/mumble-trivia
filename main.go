package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/pinheirolucas/opentrivia"
	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumbleutil"
)

type QuestionManager struct {
	Current opentrivia.Question
	Users   map[*gumble.User]*RoundUser
}

func NewQuestionManager(q opentrivia.Question, users gumble.Users) *QuestionManager {
	m := &QuestionManager{
		Current: q,
		Users:   make(map[*gumble.User]*RoundUser),
	}

	for _, u := range users {
		answers := append(q.IncorrectAnswers[:len(q.IncorrectAnswers):len(q.IncorrectAnswers)], q.CorrectAnswer)
		m.Users[u] = &RoundUser{
			Answers:       answers,
			CorrectAnswer: len(answers) - 1,
		}
		rand.Shuffle(len(answers), func(i, j int) {
			answers[i], answers[j] = answers[j], answers[i]
			if m.Users[u].CorrectAnswer == i {
				m.Users[u].CorrectAnswer = j
			} else if m.Users[u].CorrectAnswer == j {
				m.Users[u].CorrectAnswer = i
			}
		})
	}

	return m
}

func (q *QuestionManager) ResponseCount() int {
	count := 0
	for _, ru := range q.Users {
		if ru.UserResponse != nil {
			count++
		}
	}
	return count
}

func (q *QuestionManager) UserResponse(u *gumble.User, resp int) {
	if q.Users[u] == nil {
		// User wasn't given the question
		return
	}
	if q.Users[u].UserResponse != nil {
		// User already answered the question
		return
	}
	q.Users[u].UserResponse = new(int)
	*q.Users[u].UserResponse = resp
}

type RoundUser struct {
	Answers       []string
	CorrectAnswer int
	UserResponse  *int
}

func (r *RoundUser) HadCorrectAnswer() bool {
	return r.UserResponse != nil && *r.UserResponse == r.CorrectAnswer
}

type UserScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

func main() {
	scoresFile := flag.String("scores", "scores.json", "Scores storage file")
	admin := flag.String("admin", "", "User who can start and stop the bot")
	answerTime := flag.Duration("answer-time", 20*time.Second, "Amount of time users have to answer the question")
	flag.Parse()

	log.Printf("Starting Trivia!")
	var client *gumble.Client

	var answerDelay *time.Timer
	var askQuestion func()

	rand.Seed(time.Now().UnixNano())

	var m *QuestionManager

	var questions []opentrivia.Question
	triviaClient := opentrivia.NewClient(nil)

	setScores := func() {
		var userScores []*UserScore
		if contents, _ := ioutil.ReadFile(*scoresFile); contents != nil {
			json.Unmarshal(contents, &userScores)
		}

		sort.Slice(userScores, func(i, j int) bool {
			return userScores[i].Score >= userScores[j].Score
		})

		comment := "<h2>Scores:</h2><ol>"
		for _, s := range userScores {
			comment += fmt.Sprintf("<li>%s: %d</li>", s.Name, s.Score)
		}
		comment += "</ol>"

		client.Self.SetComment(comment)
	}

	incrementScores := func(names []string) {
		var userScores []*UserScore
		contents, _ := ioutil.ReadFile(*scoresFile)
		if contents != nil {
			if err := json.Unmarshal(contents, &userScores); err != nil {
				log.Println(err)
			}
		}

		processNames := make(map[string]struct{})
		for _, name := range names {
			processNames[name] = struct{}{}
		}

		for _, score := range userScores {
			if _, ok := processNames[score.Name]; ok {
				score.Score++
				delete(processNames, score.Name)
			}
		}

		for name := range processNames {
			userScores = append(userScores, &UserScore{
				Name:  name,
				Score: 1,
			})
		}

		encoded, err := json.Marshal(userScores)
		if err != nil {
			log.Println(err)
			return
		}

		if err := ioutil.WriteFile(*scoresFile, encoded, 0644); err != nil {
			log.Println(err)
		}
	}

	timeUp := func() {
		client.Do(func() {
			msg := fmt.Sprintf(`<h3><span style="color:blue">Time's up!</span> Correct answer was: %s</h3>`, m.Current.CorrectAnswer)

			var correctUsernames []string
			for user, roundUser := range m.Users {
				if roundUser.HadCorrectAnswer() {
					correctUsernames = append(correctUsernames, user.Name)
				}
			}

			if len(correctUsernames) > 0 {
				msg += "Congrats to: " + strings.Join(correctUsernames, ", ")
				incrementScores(correctUsernames)
				setScores()
			} else {
				msg += "No one got it right!"
			}

			client.Self.Channel.Send(msg, false)

			go func() {
				time.Sleep(time.Second * 3)
				askQuestion()
			}()
		})
	}

	askQuestion = func() {

		if len(questions) == 0 {
			var err error
			questions, err = triviaClient.Question.List(&opentrivia.QuestionListOptions{
				Type:  opentrivia.QuestionTypeMultiple,
				Limit: 50,
			})
			if err != nil {
				log.Fatalf("Error fetching: %s\n", err)
				return
			}
		}

		newQuestion := questions[0]
		questions = questions[1:]

		m = NewQuestionManager(newQuestion, client.Self.Channel.Users)

		client.Do(func() {
			for user, roundUser := range m.Users {
				msg := fmt.Sprintf(`<h3><u>%s</u>: %s</h3> 1: %s<br/>2: %s<br/>3: %s<br />4: %s`, m.Current.Category, m.Current.Question, roundUser.Answers[0], roundUser.Answers[1], roundUser.Answers[2], roundUser.Answers[3])
				user.Send(msg)
			}
			answerDelay = time.AfterFunc(*answerTime, timeUp)
		})
	}

	gumbleutil.Main(gumbleutil.Listener{
		Connect: func(e *gumble.ConnectEvent) {
			client = e.Client
			setScores()
		},

		TextMessage: func(e *gumble.TextMessageEvent) {
			if e.Sender == nil {
				return
			}
			if e.Sender.Name == *admin {
				switch e.Message {
				case "!stop":
					if answerDelay != nil {
						answerDelay.Stop()
						answerDelay = nil
						e.Client.Self.Channel.Send(`<h3>Trivia Stopping. Thanks for playing!</h3>`, false)
						m = nil
					}
					return
				case "!start":
					if answerDelay == nil {
						e.Client.Self.Channel.Send(`<h3>Trivia Starting! Good luck!</h3>`, false)
						askQuestion()
					}
					return
				}
			}

			if answerDelay != nil && m != nil {
				if !e.Sender.IsRegistered() {
					return
				}
				test := false
				txt := strings.TrimSpace(gumbleutil.PlainText(&e.TextMessage))
				switch txt {
				case "1":
					m.UserResponse(e.Sender, 0)
					test = true
				case "2":
					m.UserResponse(e.Sender, 1)
					test = true
				case "3":
					m.UserResponse(e.Sender, 2)
					test = true
				case "4":
					m.UserResponse(e.Sender, 3)
					test = true
				}

				if test {
					if m.ResponseCount() == len(m.Users)-1 {
						answerDelay.Stop()
						timeUp()
					}
				}
			}
		},
	})
}
