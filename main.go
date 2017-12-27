package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
  "path/filepath"

	"github.com/moorage/mtproto"
)

const updatePeriod = time.Second * 2

type Command struct {
	Name      string
	Arguments string
}

// Returns user nickname in two formats:
// <id> <First name> @<Username> <Last name> if user has username
// <id> <First name> <Last name> otherwise
func nickname(user mtproto.TL_user) string {
	if user.Username == "" {
		return fmt.Sprintf("%d %s %s", user.Id, user.First_name, user.Last_name)
	}

	return fmt.Sprintf("%d %s @%s %s", user.Id, user.First_name, user.Username, user.Last_name)
}

// Returns date in RFC822 format
func formatDate(date int32) string {
	unixTime := time.Unix((int64)(date), 0)
	return unixTime.Format(time.RFC822)
}

// Reads user input and returns Command pointer
func (cli *TelegramCLI) readCommand() *Command {
	fmt.Printf("\nUser input: ")
	input, err := cli.reader.ReadString('\n')
	if err != nil {
		fmt.Println(err)
		return nil
	}
	if input[0] != '\\' {
		return nil
	}
	command := new(Command)
	input = strings.TrimSpace(input)
	args := strings.SplitN(input, " ", 2)
	command.Name = strings.ToLower(strings.Replace(args[0], "\\", "", 1))
	if len(args) > 1 {
		command.Arguments = args[1]
	}
	return command
}

// Show help
func help() {
	fmt.Println("Available commands:")
	fmt.Println("\\me - Shows information about current account")
	fmt.Println("\\contacts - Shows contacts list")
	fmt.Println("\\umsg <id> <message> - Sends message to user with <id>")
	fmt.Println("\\cmsg <id> <message> - Sends message to chat with <id>")
	fmt.Println("\\help - Shows this message")
	fmt.Println("\\quit - Quit")
}

type TelegramCLI struct {
	mtproto   *mtproto.MTProto
	state     *mtproto.TL_updates_state
	read      chan struct{}
	stop      chan struct{}
	connected bool
	reader    *bufio.Reader
	users     map[int32]mtproto.TL_user
	chats     map[int32]mtproto.Chat
	channels  map[int32]mtproto.Channel
}

func NewTelegramCLI(pMTProto *mtproto.MTProto) (*TelegramCLI, error) {
	if pMTProto == nil {
		return nil, errors.New("NewTelegramCLI: pMTProto is nil")
	}
	cli := new(TelegramCLI)
	cli.mtproto = pMTProto
	cli.read = make(chan struct{}, 1)
	cli.stop = make(chan struct{}, 1)
	cli.reader = bufio.NewReader(os.Stdin)
	cli.users = make(map[int32]mtproto.TL_user)
	cli.chats = make(map[int32]mtproto.Chat)
	cli.channels = make(map[int32]mtproto.Channel)

	return cli, nil
}

func (cli *TelegramCLI) Authorization(phonenumber string) error {
	if phonenumber == "" {
		return fmt.Errorf("Phone number is empty")
	}
	phoneCodeHash, err := cli.mtproto.Auth_SendCode(phonenumber)
	if err != nil {
		return err
	}

	var code string
	fmt.Printf("(phone code hash "+phoneCodeHash+") Enter code: ")
	fmt.Scanf("%s", &code)
	auth, err := cli.mtproto.Auth_SignIn(phonenumber, phoneCodeHash, code)
	if err != nil {
		return err
	}

	userSelf := auth.User.(mtproto.TL_user)
	cli.users[userSelf.Id] = userSelf
	message := fmt.Sprintf("Signed in: Id %d name <%s @%s %s>\n", userSelf.Id, userSelf.First_name, userSelf.Username, userSelf.Last_name)
	fmt.Print(message)
	log.Println(message)
	log.Println(userSelf)

	return nil
}

// Load contacts to users map
func (cli *TelegramCLI) LoadContacts() error {
	_, users, err := cli.mtproto.Contacts_GetContacts(0)
	if err != nil {
		return err
	}

	for _, v := range users {
		cli.users[v.ID] = *v.TlUser
	}

	return nil
}

// Prints information about current user
func (cli *TelegramCLI) CurrentUser() error {
	user, err := cli.mtproto.Users_GetFullSelf()
	if err != nil {
		return err
	}

	cli.users[user.TlUser.Id] = *user.TlUser

	message := fmt.Sprintf("You are logged in as: %s @%s %s\nId: %d\nPhone: %s\n", user.FirstName, user.Username, user.LastName, user.ID, user.Phone)
	fmt.Print(message)
	log.Println(message)

	return nil
}

// Connects to telegram server
func (cli *TelegramCLI) Connect() error {
	if err := cli.mtproto.Connect(); err != nil {
		return err
	}
	cli.connected = true
	log.Println("Connected to telegram server")
	return nil
}

// Disconnect from telegram server
func (cli *TelegramCLI) Disconnect() error {
	if err := cli.mtproto.Disconnect(); err != nil {
		return err
	}
	cli.connected = false
	log.Println("Disconnected from telegram server")
	return nil
}

// Send signal to stop update cycle
func (cli *TelegramCLI) Stop() {
	cli.stop <- struct{}{}
}

// Send signal to read user input
func (cli *TelegramCLI) Read() {
	cli.read <- struct{}{}
}

// Run telegram cli
func (cli *TelegramCLI) Run() error {
	// Update cycle
	log.Println("CLI Update cycle started")
UpdateCycle:
	for {
		select {
		case <-cli.read:
			command := cli.readCommand()
			log.Println("User input: ")
			log.Println(*command)
			err := cli.RunCommand(command)
			if err != nil {
				log.Println(err)
			}
		case <-cli.stop:
			log.Println("Update cycle stoped")
			break UpdateCycle
		case <-time.After(updatePeriod):
			log.Println("Trying to get update from server...")
			cli.processUpdates()
		}
	}
	log.Println("CLI Update cycle finished")
	return nil
}

// Parse message and print to screen
func (cli *TelegramCLI) parseMessage(message mtproto.Message) {
	var senderName string
	from := message.From
	userFrom, found := cli.users[from]
	if !found {
		log.Printf("Can't find user with id: %d", from)
		senderName = fmt.Sprintf("%d unknow user", from)
	}
	senderName = nickname(userFrom)
	toPeer := message.To
	date := formatDate(message.Date)

	// Peer type
	switch message.To.Type {
	case mtproto.PEER_TYPE_USER:
		user, found := cli.users[message.To.ID]
		if !found {
			log.Printf("Can't find user with id: %d", message.To.ID)
			// TODO: Get information about user from telegram server
		}
		peerName := nickname(user)
		fmt.Printf("%s %d %s to %s: %s\n", date, message.ID, senderName, peerName, message.Body)
	case mtproto.PEER_TYPE_CHAT:
		chat, found := cli.chats[message.To.ID]
		if !found {
			log.Printf("Can't find chat with id: %d", message.To.ID)
		}
		fmt.Printf("%s %d %s in %s(%d): %s\n", date, message.ID, senderName, chat.Title, chat.ID, message.Body)
	case mtproto.PEER_TYPE_CHANNEL:
		channel, found := cli.channels[message.To.ID]
		if !found {
			log.Printf("Can't find channel with id: %d", message.To.ID)
		}
		fmt.Printf("%s %d %s in %s(%d): %s", date, message.ID, senderName, channel.Title, channel.ID, message.Body)
	default:
		log.Printf("Message `%v` with Unknown peer type: %v", message, toPeer)
	}
}

// Works with mtproto.TL_updates_difference and mtproto.TL_updates_differenceSlice
func (cli *TelegramCLI) parseUpdateDifference(users []mtproto.User, messages []mtproto.Message, chats []mtproto.Chat, channels []mtproto.Channel, updates []mtproto.Update) {
	// Process users
	for _, user := range users {
		cli.users[(*user.TlUser).Id] = *user.TlUser
	}
	// Process chats
	for _, chat := range chats {
		cli.chats[chat.ID] = chat
	}
	// Process Channels
	for _, channel := range channels {
		cli.channels[channel.ID] = channel
	}
	// Process messages
	for _, message := range messages {
		cli.parseMessage(message)
	}
	// Process updates
	for _, update := range updates {
		switch update.Type {
		case mtproto.UPDATE_TYPE_NEW_MESSAGE:
			cli.parseMessage(*update.Message)
		case mtproto.UPDATE_TYPE_CHANNEL_NEW_MESSAGE:
			cli.parseMessage(*update.Message)
		case mtproto.UPDATE_TYPE_EDIT_MESSAGE:
			cli.parseMessage(*update.Message)
		case mtproto.UPDATE_TYPE_EDIT_CHANNEL_MESSAGE:
			cli.parseMessage(*update.Message)
		default:
			log.Printf("Unhandled update type for update: %v\n", update)
		}
	}
}

// Parse update
func (cli *TelegramCLI) parseUpdate(update mtproto.UpdateDifference) {
	if (update.Type == mtproto.UPDATE_DIFFERENCE_EMPTY) {
		cli.state.Date = update.IntermediateState.Date
		cli.state.Seq = update.IntermediateState.Seq
		return
	}
	if (update.TlUpdatesDifference != nil) {
		cli.state = update.IntermediateState.TlUpdatesState
		cli.parseUpdateDifference(update.Users, update.NewMessages, update.Chats, update.Channels, update.OtherUpdates)
		return
	}
	if (update.Type == mtproto.UPDATE_DIFFERENCE_SLICE) {
		cli.state = update.IntermediateState.TlUpdatesState
		cli.parseUpdateDifference(update.Users, update.NewMessages, update.Chats, update.Channels, update.OtherUpdates)
		return
	}
	if (update.Type == mtproto.UPDATE_DIFFERENCE_TOO_LONG) {
		cli.state.Pts = update.IntermediateState.Pts
		return
	}
}

// Get updates and prints result
func (cli *TelegramCLI) processUpdates() {
	if cli.connected {
		if cli.state == nil {
			log.Println("cli.state is nil. Trying to get actual state...")
			tl, err := cli.mtproto.Updates_GetState()
			if err != nil {
				log.Fatal(err)
			}
			log.Println("Got something")
			log.Println(*tl)

			cli.state = tl.TlUpdatesState
			return
		}
		tl, err := cli.mtproto.Updates_GetDifference(cli.state.Pts, cli.state.Qts, cli.state.Date)
		if err != nil {
			log.Println(err)
			return
		}
		log.Println("Got new update")
		log.Println(tl)
		cli.parseUpdate(*tl)
		return
	}
}

// Print contact list
func (cli *TelegramCLI) Contacts() error {
	contacts, _, err := cli.mtproto.Contacts_GetContacts(0)
	if err != nil {
		return err
	}

	contactsMap := make(map[int32]mtproto.Contact)
	for _, contact := range contacts {
		contactsMap[contact.UserID] = contact
	}
	fmt.Printf(
		"\033[33m\033[1m%10s    %10s    %-30s\033[0m\n",
		"id", "mutual", "name",
	)
	for _, contact := range contacts {
		fmt.Printf(
			"%10d    %10t    %-30s    %-20s\n",
			contact.UserID,
			contact.Mutual,
			fmt.Sprintf("%s %s", contacts[contact.UserID].Firstname, contacts[contact.UserID].Lastname),
		)
	}

	return nil
}

// Runs command and prints result to console
func (cli *TelegramCLI) RunCommand(command *Command) error {
	switch command.Name {
	case "me":
		if err := cli.CurrentUser(); err != nil {
			return err
		}
	case "contacts":
		if err := cli.Contacts(); err != nil {
			return err
		}
	case "umsg":
		if command.Arguments == "" {
			return errors.New("Not enough arguments: peer id and msg required")
		}
		args := strings.SplitN(command.Arguments, " ", 2)
		if len(args) < 2 {
			return errors.New("Not enough arguments: peer id and msg required")
		}
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("Wrong arguments: %s isn't a number", args[0])
		}
		user, found := cli.users[int32(id)]
		if !found {
			info := fmt.Sprintf("Can't find user with id: %d", id)
			fmt.Println(info)
			return nil
		}
		update, err := cli.mtproto.Messages_SendMessage(args[1], mtproto.TL_inputPeerUser{User_id: user.Id, Access_hash: user.Access_hash}, 0)
		fmt.Printf("umsg: SendMessage Returned: %v\n", update)
		//cli.parseUpdate(*update)
	case "cmsg":
		if command.Arguments == "" {
			return errors.New("Not enough arguments: peer id and msg required")
		}
		args := strings.SplitN(command.Arguments, " ", 2)
		if len(args) < 2 {
			return errors.New("Not enough arguments: peer id and msg required")
		}
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("Wrong arguments: %s isn't a number", args[0])
		}
		update, err := cli.mtproto.Messages_SendMessage(args[1], mtproto.TL_inputPeerChat{Chat_id: int32(id)}, 0)
		fmt.Printf("cmsg: SendMessage Returned: %v\n", update)
		//cli.parseUpdate(*update)
	case "help":
		help()
	case "quit":
		cli.Stop()
		cli.Disconnect()
	default:
		fmt.Println("Unknow command. Try \\help to see all commands")
		return errors.New("Unknow command")
	}
	return nil
}

func main() {
	logfile, err := os.OpenFile("logfile.txt", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer logfile.Close()

	appId, _ := strconv.ParseUint(os.Getenv("TELEGRAM_APP_ID"), 10, 32)
	if (appId < 1) {
		log.Fatalf("couldn't parse appId in $TELEGRAM_APP_ID: `%s`", os.Getenv("TELEGRAM_APP_ID"))
	}

	apiHash := os.Getenv("TELEGRAM_API_HASH")
	if (apiHash == "") {
		log.Fatalf("couldn't parse apiHash in $TELEGRAM_API_HASH: `%s`", os.Getenv("TELEGRAM_API_HASH"))
	}

	authFile := os.Getenv("TELEGRAM_AUTH_FILE")
	if (authFile == "") {
		dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
		if err != nil {
			log.Fatal(err)
		}
		authFile = dir + "/telegram.auth"
		fmt.Println("Using auth file: " + authFile)
	}

	log.SetOutput(logfile)
	log.Println("Program started")

	// LoadContacts
	mtproto, err := mtproto.NewMTProto(int64(appId), apiHash, authFile, "", 0x01)
	if err != nil {
		log.Fatal(err)
	}
	telegramCLI, err := NewTelegramCLI(mtproto)
	if err != nil {
		log.Fatal(err)
	}
	if err = telegramCLI.Connect(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Welcome to telegram CLI")
	if err := telegramCLI.CurrentUser(); err != nil {
		var phonenumber string
		fmt.Println("Enter phonenumber number below: ")
		fmt.Scanln(&phonenumber)
		err := telegramCLI.Authorization(phonenumber)
		if err != nil {
			log.Fatal("Failed authorization", err)
		}
	}
	if err := telegramCLI.LoadContacts(); err != nil {
		log.Fatalf("Failed to load contacts: %s", err)
	}
	// Show help first time
	help()
	stop := make(chan struct{}, 1)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
	SignalProcessing:
		for {
			select {
			case <-sigc:
				telegramCLI.Read()
			case <-stop:
				break SignalProcessing
			}
		}
	}()

	err = telegramCLI.Run()
	if err != nil {
		log.Println(err)
		fmt.Println("Telegram CLI exits with error: ", err)
	}
	// Stop SignalProcessing goroutine
	stop <- struct{}{}
}
