// Command kidsroom-hub manages a fleet of kidsroom agents.
//
// It is a portable console program: it reads inventory.json from beside itself,
// asks the agents listed there what state they are in, changes what it is told
// to change and exits. Nothing is installed and nothing is left running.
//
// Exit codes: 0 — every site did what was asked, 1 — some site did not,
// 2 — the command itself was wrong and nothing was sent.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"kidsroom-agent/internal/hub"
)

const usage = `kidsroom-hub %s — управление агентами трансляции

  kidsroom-hub list                               площадки в инвентаре
               check                              проверить инвентарь, не выходя в сеть
               add <имя> <адрес> --token T        завести площадку (с проверкой связи)
               rm <имя>                           убрать из инвентаря
               status <выборка>                   сводка по площадкам
               get <имя> [-o файл]                настройки площадки (секреты как ***)
               apply <выборка> <файл>             отправить настройки из файла
               push <выборка>                     инвентарь -> площадки
               pull <выборка>                     площадки -> инвентарь
               diff <выборка>                     чем площадки расходятся с инвентарём
               set-domain <выборка> <домен>
               set-cert <выборка> --cert c.pem --key k.pem
               camera add <выборка> --id --name --url
               camera set <выборка> <id> [--name|--url|--enable|--disable]
               camera rm <выборка> <id>
               restart <выборка>                  перезапустить поток
               token show <имя>                   показать токен
               token rotate <имя>                 новый токен и запись в инвентарь
               pin <выборка> [--clear]            закрепить отпечаток сертификата
               set-control-tls <выборка> on|off   TLS на control API площадок
               version

Инвентарь (inventory.json рядом с программой) хранит и адреса площадок, и их
настройки: домен и камеры. Он — источник правды: push раскатывает его на
площадки, pull забирает с них текущее, diff показывает расхождения.

Выборка площадок — одно из:
  перечисление имён      kidsroom-hub status sad5 sad7
  все площадки           kidsroom-hub status --all
  по метке               kidsroom-hub status --tag msk

Общие флаги:
  --inventory путь   файл инвентаря (по умолчанию рядом с программой,
                     переопределяется KIDSROOM_HUB_INVENTORY)
  --json             вывод для скриптов
  --timeout 10s      ожидание ответа одной площадки
  --parallel 8       сколько площадок опрашивать одновременно
  --yes              не спрашивать подтверждения при изменении нескольких площадок
  --dry-run          показать, что было бы отправлено, и ничего не отправлять
  --stop-on-error    идти по площадкам по одной и остановиться на первой ошибке
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, hub.Version)
		os.Exit(2)
	}

	failures, err := run(os.Args[1], os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка: "+err.Error())
		os.Exit(2)
	}
	if failures > 0 {
		os.Exit(1)
	}
}

func run(cmd string, args []string) (int, error) {
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintf(os.Stdout, usage, hub.Version)
		return 0, nil
	case "version", "--version":
		fmt.Println("kidsroom-hub " + hub.Version)
		return 0, nil

	case "list":
		o, _, _, err := parse("list", args, false)
		if err != nil {
			return 0, err
		}
		return 0, hub.List(o)

	case "check":
		o, _, _, err := parse("check", args, false)
		if err != nil {
			return 0, err
		}
		return hub.Check(o)

	case "add":
		return cmdAdd(args)
	case "rm", "remove":
		return cmdRemove(args)

	case "status":
		o, sel, pos, err := parse("status", args, true)
		if err != nil {
			return 0, err
		}
		sel.Names = pos
		return hub.ShowStatus(o, *sel)

	case "push":
		o, sel, pos, err := parse("push", args, true)
		if err != nil {
			return 0, err
		}
		sel.Names = pos
		return hub.Push(o, *sel)

	case "pull":
		o, sel, pos, err := parse("pull", args, true)
		if err != nil {
			return 0, err
		}
		sel.Names = pos
		return hub.Pull(o, *sel)

	case "diff":
		o, sel, pos, err := parse("diff", args, true)
		if err != nil {
			return 0, err
		}
		sel.Names = pos
		return hub.Diff(o, *sel)

	case "get":
		return cmdGet(args)
	case "apply":
		return cmdApply(args)
	case "set-domain":
		return cmdSetDomain(args)
	case "set-cert":
		return cmdSetCert(args)
	case "camera":
		return cmdCamera(args)

	case "restart":
		o, sel, pos, err := parse("restart", args, true)
		if err != nil {
			return 0, err
		}
		sel.Names = pos
		return hub.Restart(o, *sel)

	case "token":
		return cmdToken(args)
	case "pin":
		return cmdPin(args)
	case "set-control-tls":
		return cmdSetControlTLS(args)
	}

	// Flags belong to a command, so one typed before the command would otherwise
	// be reported as an unknown command — true, but unhelpful.
	if strings.HasPrefix(cmd, "-") {
		return 0, fmt.Errorf("сначала команда, потом флаги: kidsroom-hub <команда> %s …\nсписок команд: kidsroom-hub help", cmd)
	}
	return 0, fmt.Errorf("неизвестная команда %q, список команд: kidsroom-hub help", cmd)
}

// tagList collects a repeatable --tag flag, also accepting a comma-separated
// list in one flag.
type tagList []string

func (t *tagList) String() string { return strings.Join(*t, ",") }

func (t *tagList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*t = append(*t, part)
		}
	}
	return nil
}

// parse registers the shared flags and returns them alongside the positional
// arguments.
//
// Go's flag package stops at the first non-flag argument, which would make
// `camera set sad5 cam1 --url …` unparsable. So the leading positionals are
// taken off first and whatever the parser leaves is added back: both orders
// work, which is what anyone typing these commands expects.
func parse(name string, args []string, selectable bool) (*hub.Options, *hub.Selection, []string, error) {
	o := hub.DefaultOptions()
	sel := &hub.Selection{}
	var tags tagList

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа одной площадки")
	fs.IntVar(&o.Parallel, "parallel", o.Parallel, "сколько площадок опрашивать одновременно")
	fs.BoolVar(&o.JSON, "json", false, "вывод для скриптов")
	fs.BoolVar(&o.Yes, "yes", false, "не спрашивать подтверждения")
	fs.BoolVar(&o.DryRun, "dry-run", false, "показать, что было бы отправлено")
	fs.BoolVar(&o.StopOnError, "stop-on-error", false, "остановиться на первой ошибке")
	if selectable {
		fs.BoolVar(&sel.All, "all", false, "все площадки из инвентаря")
		fs.Var(&tags, "tag", "площадки с этой меткой (можно повторять)")
	}

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return nil, nil, nil, err
	}
	sel.Tags = tags

	return o, sel, pos, nil
}

// parseInterleaved parses flags and positional arguments in any order.
//
// flag.Parse stops at the first argument that is not a flag, so
// `apply --all patch.json --yes` would leave --yes unparsed and treat it as a
// value — and the command would then look like a mistake the operator did not
// make. Parsing the remainder again after taking each positional off accepts
// every order anyone would reasonably type.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
	}
}

// splitTail peels the trailing positional that is not a site name — the domain,
// the file, the camera id. Everything before it is a list of site names, which
// may be empty when --all or --tag was used instead.
func splitTail(pos []string, what string) (names []string, tail string, err error) {
	if len(pos) == 0 {
		return nil, "", errors.New("не указано: " + what)
	}
	return pos[:len(pos)-1], pos[len(pos)-1], nil
}

func cmdAdd(args []string) (int, error) {
	o := hub.DefaultOptions()
	p := hub.AddParams{}
	var tags tagList

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа площадки")
	fs.StringVar(&p.Token, "token", "", "токен control API площадки (kidsroom-agent.exe token)")
	fs.StringVar(&p.Note, "note", "", "заметка: адрес, контакт, что угодно")
	fs.BoolVar(&p.Pin, "pin", false, "запомнить отпечаток сертификата площадки")
	fs.Var(&tags, "tag", "метка (можно повторять)")

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 0, err
	}
	if len(pos) != 2 {
		return 0, errors.New("нужно: kidsroom-hub add <имя> <адрес> --token <токен>")
	}
	p.Name, p.Addr, p.Tags = pos[0], pos[1], tags

	return 0, hub.Add(o, p)
}

func cmdRemove(args []string) (int, error) {
	o, _, pos, err := parse("rm", args, false)
	if err != nil {
		return 0, err
	}
	if len(pos) != 1 {
		return 0, errors.New("нужно: kidsroom-hub rm <имя>")
	}
	return 0, hub.Remove(o, pos[0])
}

func cmdGet(args []string) (int, error) {
	o := hub.DefaultOptions()
	var outFile string

	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа площадки")
	fs.StringVar(&outFile, "o", "", "записать в файл вместо вывода на экран")

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 0, err
	}
	if len(pos) != 1 {
		return 0, errors.New("нужно: kidsroom-hub get <имя> [-o файл]")
	}
	return 0, hub.Get(o, pos[0], outFile)
}

func cmdApply(args []string) (int, error) {
	o, sel, pos, err := parse("apply", args, true)
	if err != nil {
		return 0, err
	}
	names, file, err := splitTail(pos, "файл с настройками")
	if err != nil {
		return 0, err
	}
	sel.Names = names
	return hub.Apply(o, *sel, file)
}

func cmdSetDomain(args []string) (int, error) {
	o, sel, pos, err := parse("set-domain", args, true)
	if err != nil {
		return 0, err
	}
	names, domain, err := splitTail(pos, "домен")
	if err != nil {
		return 0, err
	}
	sel.Names = names
	return hub.SetDomain(o, *sel, domain)
}

func cmdSetCert(args []string) (int, error) {
	o := hub.DefaultOptions()
	sel := &hub.Selection{}
	var tags tagList
	var certFile, keyFile string
	var force bool

	fs := flag.NewFlagSet("set-cert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа одной площадки")
	fs.IntVar(&o.Parallel, "parallel", o.Parallel, "сколько площадок обрабатывать одновременно")
	fs.BoolVar(&o.JSON, "json", false, "вывод для скриптов")
	fs.BoolVar(&o.Yes, "yes", false, "не спрашивать подтверждения")
	fs.BoolVar(&o.DryRun, "dry-run", false, "показать план и ничего не отправлять")
	fs.BoolVar(&o.StopOnError, "stop-on-error", false, "остановиться на первой ошибке")
	fs.BoolVar(&sel.All, "all", false, "все площадки из инвентаря")
	fs.Var(&tags, "tag", "площадки с этой меткой (можно повторять)")
	fs.StringVar(&certFile, "cert", "", "файл сертификата (PEM, с цепочкой)")
	fs.StringVar(&keyFile, "key", "", "файл приватного ключа (PEM)")
	fs.BoolVar(&force, "force", false, "отправить, даже если домен площадки не входит в сертификат")

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 0, err
	}
	sel.Tags = tags
	sel.Names = pos

	if certFile == "" || keyFile == "" {
		return 0, errors.New("нужно: --cert <файл> --key <файл>")
	}
	return hub.SetCert(o, *sel, certFile, keyFile, force)
}

func cmdCamera(args []string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("нужно: kidsroom-hub camera add|set|rm …")
	}
	sub, rest := args[0], args[1:]

	o := hub.DefaultOptions()
	sel := &hub.Selection{}
	var tags tagList
	spec := hub.CameraSpec{}
	var enable, disable bool

	fs := flag.NewFlagSet("camera "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа одной площадки")
	fs.IntVar(&o.Parallel, "parallel", o.Parallel, "сколько площадок обрабатывать одновременно")
	fs.BoolVar(&o.JSON, "json", false, "вывод для скриптов")
	fs.BoolVar(&o.Yes, "yes", false, "не спрашивать подтверждения")
	fs.BoolVar(&o.DryRun, "dry-run", false, "показать, что было бы отправлено")
	fs.BoolVar(&o.StopOnError, "stop-on-error", false, "остановиться на первой ошибке")
	fs.BoolVar(&sel.All, "all", false, "все площадки из инвентаря")
	fs.Var(&tags, "tag", "площадки с этой меткой (можно повторять)")
	fs.StringVar(&spec.ID, "id", "", "идентификатор камеры (он же кусок адреса потока)")
	fs.StringVar(&spec.Name, "name", "", "название камеры для страницы")
	fs.StringVar(&spec.URL, "url", "", "RTSP-адрес камеры")
	fs.BoolVar(&enable, "enable", false, "включить камеру")
	fs.BoolVar(&disable, "disable", false, "выключить камеру")

	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return 0, err
	}
	sel.Tags = tags

	if enable && disable {
		return 0, errors.New("--enable и --disable одновременно")
	}
	if enable {
		v := true
		spec.Enabled = &v
	}
	if disable {
		v := false
		spec.Enabled = &v
	}

	switch sub {
	case "add":
		sel.Names = pos
		if spec.ID == "" || spec.Name == "" || spec.URL == "" {
			return 0, errors.New("нужно: camera add <выборка> --id <id> --name <имя> --url rtsp://…")
		}
		return hub.CameraAdd(o, *sel, spec)

	case "set":
		names, id, err := splitTail(pos, "id камеры")
		if err != nil {
			return 0, err
		}
		sel.Names = names
		return hub.CameraSet(o, *sel, id, spec)

	case "rm", "remove":
		names, id, err := splitTail(pos, "id камеры")
		if err != nil {
			return 0, err
		}
		sel.Names = names
		return hub.CameraRemove(o, *sel, id)
	}
	return 0, fmt.Errorf("неизвестная подкоманда camera %q: add, set или rm", sub)
}

func cmdToken(args []string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("нужно: kidsroom-hub token show|rotate <имя>")
	}
	sub, rest := args[0], args[1:]

	o, _, pos, err := parse("token "+sub, rest, false)
	if err != nil {
		return 0, err
	}
	if len(pos) != 1 {
		return 0, errors.New("нужно: kidsroom-hub token " + sub + " <имя>")
	}

	switch sub {
	case "show":
		return 0, hub.TokenShow(o, pos[0])
	case "rotate":
		return 0, hub.TokenRotate(o, pos[0])
	}
	return 0, fmt.Errorf("неизвестная подкоманда token %q: show или rotate", sub)
}

func cmdPin(args []string) (int, error) {
	o := hub.DefaultOptions()
	sel := &hub.Selection{}
	var tags tagList
	var clear bool

	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.InventoryPath, "inventory", o.InventoryPath, "файл инвентаря")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "ожидание ответа одной площадки")
	fs.IntVar(&o.Parallel, "parallel", o.Parallel, "сколько площадок опрашивать одновременно")
	fs.BoolVar(&o.JSON, "json", false, "вывод для скриптов")
	fs.BoolVar(&sel.All, "all", false, "все площадки из инвентаря")
	fs.Var(&tags, "tag", "площадки с этой меткой (можно повторять)")
	fs.BoolVar(&clear, "clear", false, "снять закреплённый отпечаток")

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 0, err
	}
	sel.Tags = tags
	sel.Names = pos

	return hub.Pin(o, *sel, clear)
}

func cmdSetControlTLS(args []string) (int, error) {
	o, sel, pos, err := parse("set-control-tls", args, true)
	if err != nil {
		return 0, err
	}
	names, state, err := splitTail(pos, "on или off")
	if err != nil {
		return 0, err
	}
	sel.Names = names

	var on bool
	switch strings.ToLower(state) {
	case "on", "вкл", "true", "1":
		on = true
	case "off", "выкл", "false", "0":
		on = false
	default:
		return 0, fmt.Errorf("непонятное состояние %q: нужно on или off", state)
	}
	return hub.SetControlTLS(o, *sel, on)
}
