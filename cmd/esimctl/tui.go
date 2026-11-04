package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AndreaF17/esimctl/internal/at"
	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/gnss"
	"github.com/AndreaF17/esimctl/internal/lpa"
	"github.com/AndreaF17/esimctl/internal/modem"
	"github.com/AndreaF17/esimctl/internal/qr"
	"github.com/AndreaF17/esimctl/internal/theme"
)

type tab int

const (
	tabMobile tab = iota
	tabSIM
	tabMessages
	tabESIM
	tabNotifications
	tabCount
)

var tabNames = [tabCount]string{"Mobile", "SIM", "Messages", "eSIM", "Notifications"}

// tabByName maps --tab values to tabs.
var tabByName = map[string]tab{
	"mobile": tabMobile, "sim": tabSIM, "messages": tabMessages, "sms": tabMessages,
	"esim": tabESIM, "notifications": tabNotifications,
}

type mode int

const (
	modeNormal mode = iota
	modeInput
	modeConfirm
)

type inputPurpose int

const (
	inputNickname inputPurpose = iota
	inputCode
	inputCC
	inputDelete
	inputAPN
	inputPIN
	inputPUK
	inputPUKNewPIN
	inputLockPIN
	inputOldPIN
	inputNewPIN
	inputNewPINAgain
	inputSMSNumber
	inputSMSText
)

var inputLabels = map[inputPurpose]string{
	inputNickname: "Nickname", inputCode: "Activation code", inputCC: "Confirmation code",
	inputDelete: "Type delete to confirm", inputAPN: "APN (empty or auto: automatic)",
	inputPIN: "SIM PIN", inputPUK: "PUK", inputPUKNewPIN: "New PIN",
	inputLockPIN: "SIM PIN", inputOldPIN: "Current PIN", inputNewPIN: "New PIN",
	inputNewPINAgain: "New PIN again", inputSMSNumber: "To (phone number)", inputSMSText: "Message",
}

// Mobile tab rows.
const (
	rowData = iota
	rowAPN
	rowRoaming
	rowAutoconnect
	rowRadio
	rowGPS
	mobileRows
)

type styles struct {
	title, accent, muted, text, ok, err, warn, key lipgloss.Style
}

func newStyles() styles {
	c := theme.Load()
	col := func(k string) lipgloss.Color { return lipgloss.Color(c[k]) }
	return styles{
		title:  lipgloss.NewStyle().Foreground(col("accent")).Bold(true),
		accent: lipgloss.NewStyle().Foreground(col("accent")),
		muted:  lipgloss.NewStyle().Foreground(col("dark_foreground")),
		text:   lipgloss.NewStyle().Foreground(col("foreground")),
		ok:     lipgloss.NewStyle().Foreground(col("green")),
		err:    lipgloss.NewStyle().Foreground(col("red")).Bold(true),
		warn:   lipgloss.NewStyle().Foreground(col("yellow")),
		key:    lipgloss.NewStyle().Foreground(col("accent")).Bold(true),
	}
}

type confirmReply struct {
	ok bool
	cc string
}

type (
	esimLoadedMsg struct {
		info     chipInfo
		profiles []euicc.Profile
		notifs   []euicc.Notification
		err      error
	}
	modemLoadedMsg struct {
		report modemReport
		signal modem.Signal
		gps    gnss.Fix
		gpsErr error
		err    error
	}
	opDoneMsg struct {
		msg  string
		err  error
		esim bool // the operation changed eSIM state
	}
	progressMsg struct {
		stage       string
		done, total int
	}
	confirmReqMsg struct {
		meta       lpa.ProfileMetadata
		ccRequired bool
		reply      chan confirmReply
	}
	qrMsg struct {
		code string
		err  error
	}
	messagesLoadedMsg struct {
		msgs []modem.Message
		err  error
	}
	clearStatusMsg struct{ id int }
	pollMsg        struct{}
)

type model struct {
	ctx context.Context
	st  styles
	w   int

	tab    tab
	mode   mode
	cursor [tabCount]int

	// Mobile and SIM tabs.
	mobile     modemReport
	signal     modem.Signal
	gps        gnss.Fix
	gpsErr     error
	modemErr   error
	modemReady bool
	// lockShown records that a locked SIM already moved us to the SIM tab.
	lockShown bool

	// Messages tab. reading is the message shown in full, if any.
	messages []modem.Message
	msgErr   error
	reading  *modem.Message

	// eSIM and Notifications tabs.
	info     chipInfo
	profiles []euicc.Profile
	notifs   []euicc.Notification
	esimErr  error

	busy     bool
	stage    string
	spin     spinner.Model
	status   string
	statusOK bool
	statusID int

	input   textinput.Model
	purpose inputPurpose
	// Values collected by multi-step inputs (PUK, PIN change, lock).
	pending map[string]string

	confirmText string
	onConfirm   func(m *model, yes bool) tea.Cmd

	reply chan confirmReply
	ccReq bool

	scan, paste bool   // start with a QR scan or the clipboard
	code        string // start downloading this activation code
}

var program *tea.Program

func runTUI(ctx context.Context, scan, paste bool, code, startTab string) error {
	g.quiet = true // stderr would corrupt the alternate screen
	st := newStyles()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(st.accent))
	ti := textinput.New()
	ti.CharLimit = 512
	ti.Prompt = "› "
	ti.PromptStyle = st.accent
	m := model{ctx: ctx, st: st, spin: sp, input: ti, busy: true, stage: "reading the modem",
		scan: scan, paste: paste, code: code, pending: map[string]string{}}
	if t, ok := tabByName[strings.ToLower(startTab)]; ok {
		m.tab = t
	}
	if scan || paste || code != "" {
		m.tab = tabESIM
	}
	program = tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := program.Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, loadModem(), loadESIM(), loadMessages(), poll())
}

func loadMessages() tea.Cmd {
	return func() tea.Msg {
		msgs, err := modem.Messages()
		return messagesLoadedMsg{msgs, err}
	}
}

func poll() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func loadModem() tea.Cmd {
	return func() tea.Msg {
		var r modemLoadedMsg
		r.report, r.err = getModemReport()
		if r.err != nil {
			return r
		}
		r.signal, _ = r.report.Modem.SignalDetails()
		r.gps, r.gpsErr = readFix()
		return r
	}
}

func loadESIM() tea.Cmd {
	return func() tea.Msg {
		s, err := openSession()
		if err != nil {
			return esimLoadedMsg{err: err}
		}
		defer s.Close()
		var r esimLoadedMsg
		if r.info.EID, err = s.Card.EID(); err != nil {
			return esimLoadedMsg{err: err}
		}
		if t, err := s.Card.EUICCInfo2(); err == nil {
			r.info.Info = euicc.ParseInfo2(t)
		}
		r.info.Addresses, _ = s.Card.Addresses()
		r.profiles, r.err = s.Card.Profiles()
		r.notifs, _ = s.Card.Notifications()
		return r
	}
}

// esimOp runs f in its own eUICC session.
func esimOp(msg string, f func(*lpa.Session) error) tea.Cmd {
	return func() tea.Msg {
		s, err := openSession()
		if err != nil {
			return opDoneMsg{err: err, esim: true}
		}
		err = f(s)
		s.Close()
		return opDoneMsg{msg: msg, err: err, esim: true}
	}
}

// modemOp runs f against a fresh ModemManager snapshot.
func modemOp(msg string, f func(*modem.Status) error) tea.Cmd {
	return func() tea.Msg {
		st, err := modem.Get()
		if err == nil {
			err = f(st)
		}
		return opDoneMsg{msg: msg, err: err}
	}
}

func (m *model) start(stage string, cmd tea.Cmd) tea.Cmd {
	m.busy, m.stage, m.status = true, stage, ""
	return tea.Batch(m.spin.Tick, cmd)
}

func (m *model) setStatus(text string, ok bool) tea.Cmd {
	m.status, m.statusOK = text, ok
	m.statusID++
	id := m.statusID
	if !ok {
		return nil // errors stay until the next action
	}
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{id} })
}

func (m model) selectedProfile() (euicc.Profile, bool) {
	if c := m.cursor[tabESIM]; c < len(m.profiles) {
		return m.profiles[c], true
	}
	return euicc.Profile{}, false
}

func (m model) modem() *modem.Status { return m.mobile.Modem }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w = msg.Width
		m.input.Width = max(20, msg.Width-8)
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case pollMsg:
		// Keep the Mobile and SIM tabs live; skip while the user is busy.
		if !m.busy && m.mode == modeNormal && (m.tab == tabMobile || m.tab == tabSIM) {
			return m, tea.Batch(loadModem(), poll())
		}
		if !m.busy && m.mode == modeNormal && m.tab == tabMessages {
			return m, tea.Batch(loadMessages(), poll())
		}
		return m, poll()

	case modemLoadedMsg:
		m.modemReady = true
		m.modemErr = msg.err
		if msg.err == nil {
			m.mobile, m.signal, m.gps, m.gpsErr = msg.report, msg.signal, msg.gps, msg.gpsErr
			// A locked SIM is the first thing to deal with: show the PIN row.
			if md := m.modem(); md.UnlockRequired != "" && !m.lockShown && m.tab == tabMobile && m.mode == modeNormal {
				m.lockShown = true
				m.tab = tabSIM
				for i, r := range m.simRows() {
					if r.action == "unlock" || r.action == "puk" {
						m.cursor[tabSIM] = i
					}
				}
			}
		}
		m.busy = m.busy && m.stage == "reading the modem" && m.info.EID == "" && m.esimErr == nil
		return m, nil

	case messagesLoadedMsg:
		m.msgErr = msg.err
		if msg.err == nil {
			m.messages = msg.msgs
			m.cursor[tabMessages] = min(m.cursor[tabMessages], max(0, len(m.messages)-1))
		}
		return m, nil

	case esimLoadedMsg:
		m.busy = false
		m.esimErr = msg.err
		if msg.err == nil {
			m.info, m.profiles, m.notifs = msg.info, msg.profiles, msg.notifs
			m.cursor[tabESIM] = min(m.cursor[tabESIM], max(0, len(m.profiles)-1))
			m.cursor[tabNotifications] = min(m.cursor[tabNotifications], max(0, len(m.notifs)-1))
		}
		switch {
		case m.code != "":
			c := m.code
			m.code = ""
			return m, m.download(c)
		case m.scan:
			m.scan = false
			return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		case m.paste:
			m.paste = false
			return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		}
		return m, nil

	case opDoneMsg:
		m.busy = false
		reload := []tea.Cmd{loadModem(), loadMessages()}
		if msg.esim {
			reload = append(reload, loadESIM())
		}
		if msg.err != nil {
			if errors.Is(msg.err, lpa.ErrCancelled) {
				return m, tea.Batch(append(reload, m.setStatus("download cancelled", true))...)
			}
			return m, tea.Batch(append(reload, m.setStatus(msg.err.Error(), false))...)
		}
		return m, tea.Batch(append(reload, m.setStatus(msg.msg, true))...)

	case progressMsg:
		m.stage = msg.stage
		if msg.total > 0 {
			m.stage = fmt.Sprintf("%s %d/%d", msg.stage, msg.done, msg.total)
		}
		return m, nil

	case confirmReqMsg:
		m.reply, m.ccReq = msg.reply, msg.ccRequired
		meta := msg.meta
		name := firstNonEmpty(meta.Name, "unnamed profile")
		m.confirm(fmt.Sprintf("Install %s from %s?\nICCID %s",
			m.st.title.Render(name), firstNonEmpty(meta.Provider, "unknown provider"), firstNonEmpty(meta.ICCID, "-")),
			func(m *model, yes bool) tea.Cmd {
				if yes && m.ccReq {
					return m.ask(inputCC, "confirmation code", "")
				}
				m.reply <- confirmReply{ok: yes}
				m.reply = nil
				return nil
			})
		return m, nil

	case qrMsg:
		m.busy = false
		if msg.err != nil {
			return m, m.setStatus(msg.err.Error(), false)
		}
		return m, m.download(msg.code)

	case clearStatusMsg:
		if msg.id == m.statusID {
			m.status = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) ask(p inputPurpose, placeholder, value string) tea.Cmd {
	m.purpose = p
	m.input.Placeholder = placeholder
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.EchoMode = textinput.EchoNormal
	switch p {
	case inputCC, inputPIN, inputPUK, inputPUKNewPIN, inputLockPIN, inputOldPIN, inputNewPIN, inputNewPINAgain:
		m.input.EchoMode = textinput.EchoPassword
		m.input.EchoCharacter = '•'
	}
	m.mode = modeInput
	return m.input.Focus()
}

func (m *model) confirm(text string, f func(m *model, yes bool) tea.Cmd) {
	m.confirmText, m.onConfirm = text, f
	m.mode = modeConfirm
}

func (m *model) download(code string) tea.Cmd {
	ac, err := lpa.ParseActivationCode(code)
	if err != nil {
		return m.setStatus(err.Error(), false)
	}
	m.tab = tabESIM
	ctx := m.ctx
	return m.start("contacting "+ac.SMDP, func() tea.Msg {
		s, err := openSession()
		if err != nil {
			return opDoneMsg{err: err, esim: true}
		}
		defer s.Close()
		res, err := s.Download(ctx, ac, lpa.DownloadOptions{
			Progress: func(stage string, d, t int) { program.Send(progressMsg{stage, d, t}) },
			Confirm: func(meta lpa.ProfileMetadata, ccRequired bool) (bool, string) {
				reply := make(chan confirmReply, 1)
				program.Send(confirmReqMsg{meta, ccRequired, reply})
				r := <-reply
				return r.ok, r.cc
			},
		})
		if err != nil {
			return opDoneMsg{err: err, esim: true}
		}
		return opDoneMsg{msg: "installed " + firstNonEmpty(res.Profile.Name, res.Profile.ICCID) + " — press enter to enable it", esim: true}
	})
}

var pinRE = regexp.MustCompile(`^[0-9]{4,8}$`)

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		if m.reply != nil {
			m.reply <- confirmReply{}
		}
		return m, tea.Quit
	}

	switch m.mode {
	case modeInput:
		switch k.String() {
		case "esc":
			m.mode = modeNormal
			m.input.Blur()
			clear(m.pending)
			if m.purpose == inputCC && m.reply != nil {
				m.reply <- confirmReply{}
				m.reply = nil
			}
			return m, nil
		case "enter":
			v := strings.TrimSpace(m.input.Value())
			m.mode = modeNormal
			m.input.Blur()
			return m.submit(v)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd

	case modeConfirm:
		switch strings.ToLower(k.String()) {
		case "y", "enter":
			m.mode = modeNormal
			return m, m.onConfirm(&m, true)
		case "n", "esc", "q":
			m.mode = modeNormal
			return m, m.onConfirm(&m, false)
		}
		return m, nil
	}

	if m.busy {
		if k.String() == "q" {
			return m, tea.Quit
		}
		return m, nil
	}

	if m.reading != nil {
		switch k.String() {
		case "q", "esc", "enter", "backspace":
			m.reading = nil
		case "r":
			return m, m.replyTo(*m.reading)
		}
		return m, nil
	}

	switch k.String() {
	case "q", "esc":
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % tabCount
		return m, nil
	case "shift+tab", "left", "h":
		m.tab = (m.tab + tabCount - 1) % tabCount
		return m, nil
	case "1", "2", "3", "4", "5":
		m.tab = tab(k.String()[0] - '1')
		return m, nil
	case "ctrl+r", "f5":
		return m, m.start("refreshing", tea.Batch(loadModem(), loadESIM(), loadMessages()))
	case "r":
		if m.tab == tabMessages {
			break
		}
		return m, m.start("refreshing", tea.Batch(loadModem(), loadESIM(), loadMessages()))
	case "a":
		m.tab = tabESIM
		return m, m.ask(inputCode, "LPA:1$smdp.example.com$MATCHING-ID", "")
	case "s":
		m.tab = tabESIM
		m.busy, m.stage = true, "select the QR code on screen"
		return m, tea.Batch(m.spin.Tick, func() tea.Msg { c, err := qr.FromScreen(); return qrMsg{c, err} })
	case "v":
		c, err := qr.FromClipboard()
		if err != nil {
			return m, m.setStatus(err.Error(), false)
		}
		return m, m.download(c)
	case "up", "k":
		m.cursor[m.tab] = max(0, m.cursor[m.tab]-1)
		return m, nil
	case "down", "j":
		m.cursor[m.tab] = min(m.rows()-1, m.cursor[m.tab]+1)
		return m, nil
	}

	switch m.tab {
	case tabMobile:
		return m.mobileKey(k)
	case tabSIM:
		return m.simKey(k)
	case tabMessages:
		return m.messagesKey(k)
	case tabESIM:
		return m.esimKey(k)
	}
	return m.notificationKey(k)
}

// rows returns the number of selectable rows on the current tab.
func (m model) rows() int {
	switch m.tab {
	case tabMobile:
		return mobileRows
	case tabSIM:
		return len(m.simRows())
	case tabMessages:
		return len(m.messages)
	case tabESIM:
		return len(m.profiles)
	}
	return len(m.notifs)
}

// submit handles a completed text input.
func (m model) submit(v string) (tea.Model, tea.Cmd) {
	needPIN := func(p inputPurpose, label string) (tea.Model, tea.Cmd) {
		clear(m.pending)
		return m, m.setStatus(label+" must be 4 to 8 digits", false)
	}
	switch m.purpose {
	case inputNickname:
		p, ok := m.selectedProfile()
		if !ok {
			return m, nil
		}
		return m, m.start("renaming", esimOp("nickname updated", func(s *lpa.Session) error {
			return s.Card.SetNickname(p.ICCID, v)
		}))
	case inputCode:
		if v == "" {
			return m, nil
		}
		return m, m.download(v)
	case inputCC:
		m.reply <- confirmReply{ok: true, cc: v}
		m.reply = nil
		return m, nil
	case inputDelete:
		p, ok := m.selectedProfile()
		if !ok || strings.ToLower(v) != "delete" {
			return m, m.setStatus("not deleted", true)
		}
		ctx := m.ctx
		return m, m.start("deleting "+displayName(p), esimOp("profile deleted", func(s *lpa.Session) error {
			if err := s.Card.Delete(p.ICCID); err != nil {
				return err
			}
			s.ProcessNotifications(ctx, true)
			return nil
		}))
	case inputAPN:
		set := modem.LoadSettings()
		if v == "" || strings.EqualFold(v, "auto") {
			set.AutoAPN, set.APN, set.User, set.Password = true, "", "", ""
		} else {
			set.AutoAPN, set.APN, set.User, set.Password = false, v, "", ""
		}
		return m, m.applySettings(set, "APN updated")
	case inputPIN:
		if !pinRE.MatchString(v) {
			return needPIN(inputPIN, "the PIN")
		}
		return m, m.start("unlocking the SIM", modemOp("SIM unlocked", func(st *modem.Status) error {
			if err := st.SendPIN(v); err != nil {
				return err
			}
			syncAfterSIMChange()
			return nil
		}))
	case inputPUK:
		if !regexp.MustCompile(`^[0-9]{8}$`).MatchString(v) {
			return m, m.setStatus("the PUK must be 8 digits", false)
		}
		m.pending["puk"] = v
		return m, m.ask(inputPUKNewPIN, "4 to 8 digits", "")
	case inputPUKNewPIN:
		if !pinRE.MatchString(v) {
			return needPIN(inputPUKNewPIN, "the new PIN")
		}
		puk := m.pending["puk"]
		clear(m.pending)
		return m, m.start("unblocking the SIM", modemOp("SIM unblocked, new PIN set", func(st *modem.Status) error {
			if err := st.SendPUK(puk, v); err != nil {
				return err
			}
			syncAfterSIMChange()
			return nil
		}))
	case inputLockPIN:
		if !pinRE.MatchString(v) {
			return needPIN(inputLockPIN, "the PIN")
		}
		on := m.pending["lock"] == "on"
		clear(m.pending)
		msg := "PIN request at power-on disabled"
		if on {
			msg = "PIN request at power-on enabled"
		}
		return m, m.start("updating the PIN lock", modemOp(msg, func(st *modem.Status) error { return st.SetPINLock(v, on) }))
	case inputOldPIN:
		if !pinRE.MatchString(v) {
			return needPIN(inputOldPIN, "the PIN")
		}
		m.pending["old"] = v
		return m, m.ask(inputNewPIN, "4 to 8 digits", "")
	case inputNewPIN:
		if !pinRE.MatchString(v) {
			return needPIN(inputNewPIN, "the new PIN")
		}
		m.pending["new"] = v
		return m, m.ask(inputNewPINAgain, "repeat the new PIN", "")
	case inputSMSNumber:
		if v == "" {
			return m, nil
		}
		m.pending["to"] = v
		return m, m.ask(inputSMSText, "text of the message", "")
	case inputSMSText:
		to := m.pending["to"]
		clear(m.pending)
		if v == "" {
			return m, m.setStatus("message not sent", true)
		}
		return m, m.start("sending to "+to, func() tea.Msg {
			return opDoneMsg{msg: "message sent to " + to, err: modem.SendMessage(to, v)}
		})
	case inputNewPINAgain:
		old, nw := m.pending["old"], m.pending["new"]
		clear(m.pending)
		if v != nw {
			return m, m.setStatus("the two new PINs don't match", false)
		}
		return m, m.start("changing the PIN", modemOp("PIN changed", func(st *modem.Status) error { return st.ChangePIN(old, nw) }))
	}
	return m, nil
}

func (m *model) applySettings(set modem.Settings, msg string) tea.Cmd {
	return m.start("updating mobile data settings", func() tea.Msg {
		if err := modem.SaveSettings(set); err != nil {
			return opDoneMsg{err: err}
		}
		st, _ := modem.Get()
		r, err := modem.Apply(st, set)
		if err == nil && r.APN != "" && set.AutoAPN {
			msg += fmt.Sprintf(" (automatic: %s)", r.APN)
		}
		return opDoneMsg{msg: msg, err: err}
	})
}

func (m model) mobileKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	md := m.modem()
	switch k.String() {
	case "o":
		if m.gps.Valid {
			url := fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", m.gps.Lat, m.gps.Lon, m.gps.Lat, m.gps.Lon)
			_ = exec.Command("xdg-open", url).Start()
		}
		return m, nil
	case "enter", " ":
	default:
		return m, nil
	}
	if md == nil {
		return m, m.setStatus("no modem", false)
	}
	set := m.mobile.Settings
	switch m.cursor[tabMobile] {
	case rowData:
		if m.mobile.Connection.Active {
			return m, m.start("disconnecting", modemOp("mobile data disconnected", func(*modem.Status) error { return modem.Disconnect() }))
		}
		return m, m.start("connecting", func() tea.Msg {
			st, err := modem.Get()
			if err != nil {
				return opDoneMsg{err: err}
			}
			r, err := modem.Connect(st)
			msg := "mobile data connected"
			if r.APN != "" {
				msg += " (APN " + r.APN + ")"
			}
			return opDoneMsg{msg: msg, err: err}
		})
	case rowAPN:
		cur := ""
		if !set.AutoAPN {
			cur = set.APN
		}
		return m, m.ask(inputAPN, "auto", cur)
	case rowRoaming:
		set.Roaming = !set.Roaming
		return m, m.applySettings(set, "roaming "+onOff(set.Roaming))
	case rowAutoconnect:
		set.Autoconnect = !set.Autoconnect
		return m, m.applySettings(set, "autoconnect "+onOff(set.Autoconnect))
	case rowRadio:
		on := !radioOn(md)
		return m, m.start("turning the radio "+onOff(on), modemOp("radio "+onOff(on), func(st *modem.Status) error {
			if err := st.SetEnabled(on); err != nil {
				return explainEnable(err)
			}
			return nil
		}))
	case rowGPS:
		on := !m.gps.Enabled
		return m, m.start("turning GPS "+onOff(on), func() tea.Msg {
			err := withAT(func(p *at.Port) error { return gnss.SetEnabled(p, on) })
			return opDoneMsg{msg: "GPS " + onOff(on), err: err}
		})
	}
	return m, nil
}

// simRow is one actionable line of the SIM tab.
type simRow struct {
	slot   int // > 0: switch to this slot
	action string
}

func (m model) simRows() []simRow {
	md := m.modem()
	if md == nil {
		return nil
	}
	var rows []simRow
	for _, s := range md.Slots {
		rows = append(rows, simRow{slot: s.Index})
	}
	switch {
	case md.UnlockRequired == "sim-puk":
		rows = append(rows, simRow{action: "puk"})
	case md.UnlockRequired != "":
		rows = append(rows, simRow{action: "unlock"})
	}
	// PIN settings only make sense for a usable SIM (not an empty eSIM).
	if s, ok := md.ActiveSlot(); ok && s.ICCID != "" && md.UnlockRequired == "" {
		rows = append(rows, simRow{action: "lock"}, simRow{action: "change"})
	}
	return rows
}

func (m model) simKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() != "enter" && k.String() != " " {
		return m, nil
	}
	rows := m.simRows()
	c := m.cursor[tabSIM]
	if c >= len(rows) {
		return m, nil
	}
	md := m.modem()
	r := rows[c]
	switch {
	case r.slot > 0:
		if r.slot == md.PrimarySlot {
			return m, m.setStatus(fmt.Sprintf("slot %d is already in use", r.slot), true)
		}
		n := r.slot
		m.confirm(fmt.Sprintf("Switch to slot %d? The modem restarts and mobile data drops for a moment.", n), func(m *model, yes bool) tea.Cmd {
			if !yes {
				return nil
			}
			return m.start(fmt.Sprintf("switching to slot %d", n), modemOp(fmt.Sprintf("switched to slot %d", n), func(st *modem.Status) error {
				if err := st.SetPrimarySlot(n); err != nil {
					return err
				}
				syncAfterSIMChange()
				return nil
			}))
		})
	case r.action == "unlock":
		return m, m.ask(inputPIN, "4 to 8 digits", "")
	case r.action == "puk":
		return m, m.ask(inputPUK, "8 digits, from the SIM card holder", "")
	case r.action == "lock":
		m.pending["lock"] = onOff(!md.PINLock)
		return m, m.ask(inputLockPIN, "current PIN", "")
	case r.action == "change":
		return m, m.ask(inputOldPIN, "current PIN", "")
	}
	return m, nil
}

func (m model) esimKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p, ok := m.selectedProfile()
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter", " ":
		name := displayName(p)
		if p.Enabled {
			return m, m.start("disabling "+name, switchCmd(m.ctx, "disable", p.ICCID))
		}
		return m, m.start("enabling "+name, switchCmd(m.ctx, "enable", p.ICCID))
	case "n":
		return m, m.ask(inputNickname, "nickname (empty to clear)", p.Nickname)
	case "d", "x", "delete":
		if p.Enabled {
			return m, m.setStatus("disable the profile before deleting it", false)
		}
		return m, m.ask(inputDelete, "delete", "")
	}
	return m, nil
}

func (m model) notificationKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "p", "enter":
		if len(m.notifs) == 0 {
			return m, nil
		}
		ctx := m.ctx
		return m, m.start("sending notifications", func() tea.Msg {
			s, err := openSession()
			if err != nil {
				return opDoneMsg{err: err, esim: true}
			}
			defer s.Close()
			n, errs := s.ProcessNotifications(ctx, true)
			if len(errs) > 0 {
				return opDoneMsg{err: fmt.Errorf("%d sent, %d failed: %v", n, len(errs), errs[0]), esim: true}
			}
			return opDoneMsg{msg: fmt.Sprintf("%d notification(s) sent", n), esim: true}
		})
	case "x", "d", "delete":
		c := m.cursor[tabNotifications]
		if c >= len(m.notifs) {
			return m, nil
		}
		n := m.notifs[c]
		m.confirm(fmt.Sprintf("Drop notification %d without sending it?", n.Seq), func(m *model, yes bool) tea.Cmd {
			if !yes {
				return nil
			}
			return m.start("removing", esimOp("notification removed", func(s *lpa.Session) error {
				return s.Card.RemoveNotification(n.Seq)
			}))
		})
	}
	return m, nil
}

func (m *model) replyTo(msg modem.Message) tea.Cmd {
	m.reading = nil
	m.pending["to"] = msg.Number
	return m.ask(inputSMSText, "reply to "+msg.Number, "")
}

func (m model) messagesKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "c" || k.String() == "n" {
		return m, m.ask(inputSMSNumber, "+39…", "")
	}
	c := m.cursor[tabMessages]
	if c >= len(m.messages) {
		return m, nil
	}
	msg := m.messages[c]
	switch k.String() {
	case "enter", " ":
		m.reading = &msg
		if !msg.Read {
			modem.MarkRead(msg)
			m.messages[c].Read = true
		}
	case "r":
		return m, m.replyTo(msg)
	case "d", "x", "delete":
		m.confirm(fmt.Sprintf("Delete the message from %s?", msg.Number), func(m *model, yes bool) tea.Cmd {
			if !yes {
				return nil
			}
			return m.start("deleting", func() tea.Msg {
				return opDoneMsg{msg: "message deleted", err: modem.DeleteMessage(msg.ID)}
			})
		})
	}
	return m, nil
}

func switchCmd(ctx context.Context, verb, iccid string) tea.Cmd {
	return func() tea.Msg {
		s, err := openSession()
		if err != nil {
			return opDoneMsg{err: err, esim: true}
		}
		if verb == "enable" {
			err = s.Card.Enable(iccid, true)
		} else {
			err = s.Card.Disable(iccid, true)
		}
		s.Close()
		if err != nil {
			return opDoneMsg{err: err, esim: true}
		}
		program.Send(progressMsg{stage: "waiting for the modem"})
		notifyAfterReset(ctx)
		// The SIM changed: point the automatic APN at the new profile.
		if st, err := modem.Get(); err == nil {
			_, _ = modem.Apply(st, modem.LoadSettings())
		}
		return opDoneMsg{msg: "profile " + verb + "d", esim: true}
	}
}

func radioOn(md *modem.Status) bool {
	switch md.State {
	case "", "disabled", "disabling", "failed", "locked", "initializing":
		return false
	}
	return true
}

func displayName(p euicc.Profile) string {
	return firstNonEmpty(p.Nickname, p.Name, p.Provider, p.ICCID)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- rendering ----

func (m model) View() string {
	st := m.st
	var b strings.Builder
	w := m.w
	if w == 0 {
		w = 80
	}

	b.WriteString("\n " + st.title.Render("Mobile") + "  " + st.muted.Render(m.summary()) + "\n\n ")
	for i := tab(0); i < tabCount; i++ {
		name := tabNames[i]
		if i == tabNotifications && len(m.notifs) > 0 {
			name += fmt.Sprintf(" (%d)", len(m.notifs))
		}
		if n := modem.Unread(m.messages); i == tabMessages && n > 0 {
			name += fmt.Sprintf(" (%d)", n)
		}
		label := fmt.Sprintf("%d %s", i+1, name)
		if i == m.tab {
			b.WriteString(st.accent.Underline(true).Render(label))
		} else {
			b.WriteString(st.muted.Render(label))
		}
		b.WriteString("   ")
	}
	b.WriteString("\n\n")

	switch m.tab {
	case tabMobile:
		b.WriteString(m.viewMobile())
	case tabSIM:
		b.WriteString(m.viewSIM())
	case tabMessages:
		b.WriteString(m.viewMessages(w))
	case tabESIM:
		b.WriteString(m.viewProfiles())
	case tabNotifications:
		b.WriteString(m.viewNotifications())
	}

	b.WriteString("\n")
	switch m.mode {
	case modeInput:
		b.WriteString(" " + st.text.Render(inputLabels[m.purpose]) + "\n " + m.input.View() + "\n")
		b.WriteString(" " + help(st, w, "enter", "confirm", "esc", "cancel") + "\n")
	case modeConfirm:
		for _, line := range strings.Split(m.confirmText, "\n") {
			b.WriteString(" " + st.text.Render(line) + "\n")
		}
		b.WriteString(" " + help(st, w, "y", "yes", "n", "no") + "\n")
	default:
		switch {
		case m.busy:
			b.WriteString(" " + m.spin.View() + " " + st.text.Render(m.stage) + "…\n")
		case m.status != "" && m.statusOK:
			b.WriteString(" " + st.ok.Render("✓ "+m.status) + "\n")
		case m.status != "":
			b.WriteString(" " + st.err.Render("✗ ") + st.text.Render(wrap(m.status, w-4)) + "\n")
		default:
			b.WriteString("\n")
		}
		b.WriteString("\n " + help(st, w, m.hints()...) + "\n")
	}
	return b.String()
}

func (m model) hints() []string {
	common := []string{"tab", "next tab", "r", "refresh", "q", "quit"}
	switch m.tab {
	case tabMobile:
		h := []string{"enter", "toggle / edit"}
		if m.gps.Valid {
			h = append(h, "o", "open map")
		}
		return append(h, common...)
	case tabSIM:
		return append([]string{"enter", "select"}, common...)
	case tabMessages:
		if m.reading != nil {
			return []string{"r", "reply", "esc", "back"}
		}
		return []string{"enter", "read", "r", "reply", "c", "new", "d", "delete", "tab", "next tab", "q", "quit"}
	case tabESIM:
		return append([]string{"enter", "enable/disable", "n", "rename", "d", "delete", "a", "add code",
			"s", "scan QR", "v", "paste"}, common...)
	}
	return append([]string{"p", "send all", "x", "drop"}, common...)
}

func (m model) summary() string {
	md := m.modem()
	switch {
	case m.modemErr != nil:
		return "modem unavailable"
	case md == nil:
		return "looking for the modem…"
	case md.UnlockRequired != "":
		return "SIM locked: " + md.UnlockRequired + " required"
	case !radioOn(md):
		return "radio off"
	case md.Operator == "":
		return "searching for a network"
	}
	s := fmt.Sprintf("%s · %s · %d%%", md.Operator, strings.ToUpper(strings.Join(md.AccessTech, "/")), md.Signal)
	if md.Roaming {
		s += " · roaming"
	}
	if m.mobile.Connection.Active {
		s += " · connected"
	}
	return s
}

func (m model) cursorBar(t tab, i int) string {
	if m.cursor[t] == i {
		return m.st.accent.Render("▌")
	}
	return " "
}

// row renders a selectable line with a label, a value and a detail.
func (m model) row(t tab, i int, label, value, detail string) string {
	st := m.st
	line := fmt.Sprintf("%s %s %s", m.cursorBar(t, i), st.text.Render(fmt.Sprintf("%-16s", label)), value)
	if detail != "" {
		line += "  " + st.muted.Render(detail)
	}
	return line + "\n"
}

func (m model) toggle(on bool) string {
	if on {
		return m.st.ok.Render("● on ")
	}
	return m.st.muted.Render("○ off")
}

func (m model) viewMobile() string {
	st := m.st
	md := m.modem()
	if m.modemErr != nil {
		return " " + st.err.Render("✗ ") + st.text.Render(m.modemErr.Error()) + "\n"
	}
	if md == nil {
		return ""
	}
	var b strings.Builder
	kv := func(k, v string) {
		b.WriteString(fmt.Sprintf("   %s %s\n", st.muted.Render(fmt.Sprintf("%-15s", k)), st.text.Render(v)))
	}
	kv("Modem", fmt.Sprintf("%s %s", md.Manufacturer, md.Model))
	kv("State", md.State+", power "+md.Power)
	if md.Operator != "" {
		net := fmt.Sprintf("%s (%s), %s", md.Operator, md.OperatorCode, strings.ToUpper(strings.Join(md.AccessTech, "/")))
		if md.Roaming {
			net += ", roaming"
		}
		kv("Network", net)
		sig := fmt.Sprintf("%d%%", md.Signal)
		if m.signal.Tech != "" {
			sig += fmt.Sprintf("  RSRP %.0f dBm  RSRQ %.1f dB  SNR %.1f dB", m.signal.RSRP, m.signal.RSRQ, m.signal.SNR)
		}
		kv("Signal", sig)
	} else if radioOn(md) {
		kv("Network", "searching…")
	}
	if m.gps.Valid {
		kv("Location", fmt.Sprintf("%.6f, %.6f  ±%.1f HDOP, %.0f m", m.gps.Lat, m.gps.Lon, m.gps.HDOP, m.gps.Alt))
	}
	b.WriteString("\n")

	c := m.mobile.Connection
	set := m.mobile.Settings
	dataDetail := ""
	switch {
	case c.Active:
		dataDetail = strings.TrimSpace(c.IPv4 + "  " + c.IPv6)
	case md.Operator == "":
		dataDetail = "no network"
	}
	b.WriteString(m.row(tabMobile, rowData, "Mobile data", m.toggle(c.Active), dataDetail))
	apn, apnDetail := "automatic", ""
	if !set.AutoAPN {
		apn = set.APN
	} else if a := m.mobile.SuggestedAPN; a != nil {
		apnDetail = a.Name + " · " + a.Provider
	} else if md.Operator != "" || len(md.Slots) > 0 {
		apnDetail = "chosen by NetworkManager"
	}
	b.WriteString(m.row(tabMobile, rowAPN, "APN", st.text.Render(apn), apnDetail))
	b.WriteString(m.row(tabMobile, rowRoaming, "Data roaming", m.toggle(set.Roaming), ""))
	b.WriteString(m.row(tabMobile, rowAutoconnect, "Autoconnect", m.toggle(set.Autoconnect), ""))
	b.WriteString(m.row(tabMobile, rowRadio, "Cellular radio", m.toggle(radioOn(md)), ""))
	gpsDetail := ""
	switch {
	case m.gpsErr != nil:
		gpsDetail = m.gpsErr.Error()
	case m.gps.Valid:
		gpsDetail = fmt.Sprintf("%.5f, %.5f  %s, %d/%d sats, ±%.1f HDOP", m.gps.Lat, m.gps.Lon,
			strings.ToUpper(m.gps.Mode), m.gps.SatsUsed, m.gps.SatsInView, m.gps.HDOP)
	case m.gps.Enabled:
		gpsDetail = fmt.Sprintf("searching, %d satellites in view", m.gps.SatsInView)
	}
	b.WriteString(m.row(tabMobile, rowGPS, "GPS", m.toggle(m.gps.Enabled), gpsDetail))
	return b.String()
}

func (m model) viewSIM() string {
	st := m.st
	md := m.modem()
	if m.modemErr != nil {
		return " " + st.err.Render("✗ ") + st.text.Render(m.modemErr.Error()) + "\n"
	}
	if md == nil {
		return ""
	}
	var b strings.Builder
	for i, r := range m.simRows() {
		if r.slot > 0 {
			s := md.Slots[r.slot-1]
			dot := st.muted.Render("○")
			if s.Index == md.PrimarySlot {
				dot = st.ok.Render("●")
			}
			desc := st.muted.Render("empty")
			if s.Present {
				kind := "SIM card"
				if s.Type == "esim" {
					kind = "eSIM"
				}
				desc = st.text.Render(kind)
				if s.Operator != "" {
					desc += st.muted.Render(" · " + s.Operator)
				} else if s.Type == "esim" && s.ICCID == "" {
					desc += st.muted.Render(" · no profile enabled")
				}
			}
			b.WriteString(fmt.Sprintf("%s%s %s  %s\n", m.cursorBar(tabSIM, i), dot, st.text.Render(fmt.Sprintf("Slot %d", s.Index)), desc))
			if s.Present && s.Index == md.PrimarySlot {
				var details []string
				if s.ICCID != "" {
					details = append(details, "ICCID "+s.ICCID)
				}
				if s.HomeCode != "" {
					details = append(details, "network "+s.HomeCode)
				}
				if s.EID != "" {
					details = append(details, "EID "+s.EID)
				}
				if len(details) > 0 {
					b.WriteString("     " + st.muted.Render(strings.Join(details, " · ")) + "\n")
				}
			}
			continue
		}
		if i > 0 && m.simRows()[i-1].slot > 0 {
			b.WriteString("\n")
		}
		switch r.action {
		case "unlock":
			left := ""
			if md.UnlockRetries > 0 {
				left = fmt.Sprintf("%d attempts left", md.UnlockRetries)
			}
			b.WriteString(m.row(tabSIM, i, "Enter PIN", st.warn.Render(md.UnlockRequired+" required"), left))
		case "puk":
			b.WriteString(m.row(tabSIM, i, "Enter PUK", st.err.Render("SIM blocked"), "the PUK is on the SIM card holder"))
		case "lock":
			b.WriteString(m.row(tabSIM, i, "PIN at power-on", m.toggle(md.PINLock), "asks for the PIN when the modem starts"))
		case "change":
			b.WriteString(m.row(tabSIM, i, "Change PIN", "", ""))
		}
	}
	if len(md.Slots) == 0 {
		b.WriteString(" " + st.muted.Render("The modem reports no SIM slots.") + "\n")
	}
	return b.String()
}

func (m model) viewMessages(w int) string {
	st := m.st
	if m.msgErr != nil {
		return " " + st.err.Render("✗ ") + st.text.Render(m.msgErr.Error()) + "\n"
	}
	if r := m.reading; r != nil {
		var b strings.Builder
		dir := "From"
		if !r.Incoming {
			dir = "To"
		}
		b.WriteString(fmt.Sprintf("   %s %s  %s\n\n", st.muted.Render(dir), st.title.Render(r.Number), st.muted.Render(when(r.Time))))
		for _, line := range wrapText(r.Text, max(20, w-6)) {
			b.WriteString("   " + st.text.Render(line) + "\n")
		}
		return b.String()
	}
	if len(m.messages) == 0 {
		return " " + st.muted.Render("No messages. Press c to write one.") + "\n"
	}
	var b strings.Builder
	for i, msg := range m.messages {
		dot := " "
		if !msg.Read {
			dot = st.accent.Render("●")
		}
		who := msg.Number
		if !msg.Incoming {
			who = "→ " + who
		}
		name := st.text.Bold(!msg.Read).Render(fmt.Sprintf("%-18s", who))
		preview := firstLine(msg.Text, max(10, w-36))
		b.WriteString(fmt.Sprintf("%s%s %s %s  %s\n", m.cursorBar(tabMessages, i), dot, name,
			st.muted.Render(fmt.Sprintf("%-12s", when(msg.Time))), st.text.Render(preview)))
	}
	return b.String()
}

// wrapText breaks s into lines of at most w runes, at spaces when possible.
func wrapText(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len([]rune(word)) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

func (m model) viewProfiles() string {
	st := m.st
	var b strings.Builder
	if m.info.EID != "" {
		b.WriteString("   " + st.muted.Render(fmt.Sprintf("EID %s · %d KiB free", m.info.EID, m.info.Info.FreeNVM/1024)) + "\n\n")
	}
	var inactive ErrESIMInactive
	if errors.As(m.esimErr, &inactive) {
		return b.String() + " " + st.text.Render(fmt.Sprintf("The eSIM is in slot %d, which is not in use: the modem powers one SIM at a time.", inactive.Slot)) +
			"\n " + st.muted.Render("Switch to it from the SIM tab to manage eSIM profiles.") + "\n"
	}
	if m.esimErr != nil {
		return b.String() + " " + st.err.Render("✗ ") + st.text.Render(m.esimErr.Error()) + "\n"
	}
	if len(m.profiles) == 0 {
		if m.info.EID == "" {
			return b.String()
		}
		return b.String() + " " + st.muted.Render("No profiles. Press s to scan your carrier's QR code, or a to type the activation code.") + "\n"
	}
	for i, p := range m.profiles {
		dot := st.muted.Render("○")
		if p.Enabled {
			dot = st.ok.Render("●")
		}
		name := st.text.Bold(p.Enabled).Render(displayName(p))
		tag := ""
		if p.Class != "operational" {
			tag = st.warn.Render(" " + p.Class)
		}
		provider := ""
		if p.Provider != "" && p.Provider != displayName(p) {
			provider = st.muted.Render("  " + p.Provider)
		}
		bar := m.cursorBar(tabESIM, i)
		b.WriteString(fmt.Sprintf("%s%s %s%s%s\n%s  %s\n", bar, dot, name, provider, tag, bar, st.muted.Render(p.ICCID)))
	}
	return b.String()
}

func (m model) viewNotifications() string {
	st := m.st
	if len(m.notifs) == 0 {
		return " " + st.muted.Render("No pending notifications.") + "\n"
	}
	var b strings.Builder
	for i, n := range m.notifs {
		b.WriteString(fmt.Sprintf("%s%s %s %s  %s\n", m.cursorBar(tabNotifications, i), st.muted.Render(fmt.Sprintf("#%-3d", n.Seq)),
			st.accent.Render(fmt.Sprintf("%-8s", n.Operation)), st.text.Render(firstNonEmpty(n.ICCID, "-")), st.muted.Render(n.Address)))
	}
	return b.String()
}

// help renders key hints, wrapping them to the terminal width.
func help(st styles, w int, pairs ...string) string {
	sep := st.muted.Render("  ·  ")
	var lines []string
	line, lineW := "", 0
	for i := 0; i+1 < len(pairs); i += 2 {
		part := st.key.Render(pairs[i]) + " " + st.muted.Render(pairs[i+1])
		pw := len(pairs[i]) + 1 + len([]rune(pairs[i+1]))
		if lineW > 0 && lineW+5+pw > w-2 {
			lines = append(lines, line)
			line, lineW = "", 0
		}
		if lineW > 0 {
			line += sep
			lineW += 5
		}
		line += part
		lineW += pw
	}
	return strings.Join(append(lines, line), "\n ")
}

func wrap(s string, w int) string {
	if w < 20 || len(s) <= w {
		return s
	}
	return s[:w-1] + "…"
}
