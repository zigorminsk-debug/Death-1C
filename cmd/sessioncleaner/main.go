// SessionCleaner — программа очистки старых и задублированных сеансов
// на сервере 1С:Предприятие.
//
// Обычный запуск (по ярлыку): открывается окно со списком сеансов.
// Запуск с удерживаемым Shift: открывается меню настроек.
// Консольный запуск (-clean -yes): очистка по расписанию из планировщика заданий.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/app"
	"github.com/zigorminsk-debug/death-1c/internal/buildinfo"
	"github.com/zigorminsk-debug/death-1c/internal/cleaner"
	"github.com/zigorminsk-debug/death-1c/internal/platform"
	"github.com/zigorminsk-debug/death-1c/internal/ui"
)

// singleInstanceKey — имя системного мьютекса: второй экземпляр программы
// не запускается, чтобы два окна не завершали сеансы одновременно.
const singleInstanceKey = "Death1C.SessionCleaner.SingleInstance"

// idleLimit — сколько программа ждёт закрытия окна интерфейса,
// прежде чем завершиться самостоятельно.
const idleLimit = 5 * time.Minute

type options struct {
	settings    bool
	noWindow    bool
	listen      string
	dataDir     string
	showVersion bool
	console     bool
	clean       bool
	dryRun      bool
	yes         bool
	noElevate   bool
	report      string
}

func main() { os.Exit(run()) }

func run() int {
	opts := parseFlags()

	if opts.showVersion {
		//_exe собран с подсистемой GUI: без подключения к консоли родителя
		// вывод в командной строке был бы невидим.
		platform.AttachConsole()
		fmt.Println(buildinfo.OneLine())
		fmt.Printf("Разработчик: %s, %s, %s\n",
			buildinfo.DeveloperName, buildinfo.DeveloperEmail, buildinfo.DeveloperPhone)
		return 0
	}

	// Shift определяем в самом начале: дальше возможны диалоги UAC,
	// после которых состояние клавиши уже не отражает намерение пользователя.
	shift := platform.ShiftPressedWithin(400 * time.Millisecond)

	if opts.console || opts.clean {
		return runCLI(opts)
	}
	return runGUI(opts, shift)
}

func parseFlags() options {
	var o options
	flag.BoolVar(&o.settings, "settings", false, "открыть меню настроек")
	flag.BoolVar(&o.noWindow, "no-window", false, "не открывать окно интерфейса (только локальный сервер)")
	flag.StringVar(&o.listen, "listen", "127.0.0.1:0", "адрес локального сервера интерфейса")
	flag.StringVar(&o.dataDir, "data-dir", "", "каталог настроек и журнала (по умолчанию %ProgramData%\\1C Session Cleaner)")
	flag.BoolVar(&o.showVersion, "version", false, "показать сведения о сборке и завершиться")
	flag.BoolVar(&o.console, "console", false, "консольный режим: показать сеансы списком и завершиться")
	flag.BoolVar(&o.clean, "clean", false, "консольный режим: удалить старые сеансы (нужен также -yes)")
	flag.BoolVar(&o.dryRun, "dry-run", false, "показать, какие сеансы были бы завершены, но не завершать их")
	flag.BoolVar(&o.yes, "yes", false, "подтвердить завершение сеансов в консольном режиме")
	flag.BoolVar(&o.noElevate, "no-elevate", false, "не запрашивать права администратора при запуске")
	flag.StringVar(&o.report, "report", "", "сохранить текстовый отчёт в указанный файл")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s, %s\n\n", buildinfo.ProductName, buildinfo.FullVersion())
		fmt.Fprintf(os.Stderr, "Разработчик: %s <%s>, %s\n\nИспользование:\n",
			buildinfo.DeveloperName, buildinfo.DeveloperEmail, buildinfo.DeveloperPhone)
		fmt.Fprintf(os.Stderr, "  %s                    запуск окна со списком сеансов\n", buildinfo.ProductExe)
		fmt.Fprintf(os.Stderr, "  %s -settings          запуск сразу с меню настроек (то же, что Shift при запуске)\n", buildinfo.ProductExe)
		fmt.Fprintf(os.Stderr, "  %s -clean -yes        удалить старые сеансы без окна (для планировщика заданий)\n", buildinfo.ProductExe)
		fmt.Fprintf(os.Stderr, "  %s -console           напечатать список сеансов и завершиться\n\nПараметры:\n", buildinfo.ProductExe)
		flag.PrintDefaults()
	}
	flag.Parse()
	return o
}

// runGUI — основной режим: локальный сервер интерфейса и окно программы.
func runGUI(opts options, shift bool) int {
	release, first := platform.SingleInstance(singleInstanceKey)
	if !first {
		platform.MessageBox(buildinfo.ProductName,
			"Программа уже запущена.\n\nВторой экземпляр не открывается: одновременная очистка сеансов\n"+
				"из двух окон может привести к конфликтам.\n\nНайдите открытое окно «"+buildinfo.ProductName+"» на панели задач.")
		return 0
	}
	defer release()

	if !platform.IsElevated() && !opts.noElevate {
		var extra []string
		if shift || opts.settings {
			extra = append(extra, "-settings")
		}
		if err := platform.RelaunchElevated(extra...); err == nil {
			return 0
		}
		platform.MessageBox(buildinfo.ProductName,
			"Не удалось получить права администратора.\n"+
				"Программа продолжит работу, но часть операций будет недоступна.")
	}

	a, err := app.New(opts.dataDir)
	if err != nil {
		platform.MessageBox(buildinfo.ProductName, "Не удалось запустить программу:\n"+err.Error())
		return 1
	}
	defer a.Close()

	startView := "main"
	if opts.settings || shift || a.FirstRun() || !a.State().Configured {
		startView = "settings"
	}

	srv, err := ui.New(a, startView)
	if err != nil {
		platform.MessageBox(buildinfo.ProductName, "Не удалось подготовить интерфейс:\n"+err.Error())
		return 1
	}
	url, err := srv.Start(opts.listen)
	if err != nil {
		platform.MessageBox(buildinfo.ProductName, "Не удалось запустить локальный сервер интерфейса:\n"+err.Error())
		return 1
	}

	if !opts.noWindow {
		if err := platform.OpenWindow(url, a.Config().UI.WindowMode); err != nil {
			a.Log().Warn("окно интерфейса не открылось автоматически: %v", err)
			platform.MessageBox(buildinfo.ProductName,
				"Не удалось открыть окно автоматически.\nОткройте адрес в браузере вручную:\n"+url)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-srv.Quit():
			a.Log().Info("выход по команде из интерфейса")
			return 0
		case s := <-sig:
			a.Log().Info("выход по сигналу %v", s)
			return 0
		case <-ticker.C:
			if opts.noWindow {
				continue
			}
			if time.Since(srv.LastActivity()) > idleLimit {
				a.Log().Info("окно интерфейса закрыто — программа завершается")
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = srv.Shutdown(ctx)
				cancel()
				return 0
			}
		}
	}
}

// runCLI — консольный режим: отчёт по сеансам и очистка по расписанию.
func runCLI(opts options) int {
	platform.AttachConsole()

	a, err := app.New(opts.dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	plan, err := a.Refresh(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось получить сеансы:", err)
		return 2
	}

	report := formatReport(plan)
	fmt.Println(report)

	if opts.clean && !opts.dryRun {
		if !opts.yes && a.Config().Policy.Confirm {
			fmt.Fprintln(os.Stderr, "Сеансы не завершены: для очистки по расписанию добавьте параметр -yes.")
			writeReport(opts.report, report)
			return 3
		}
		res, err := a.Execute(ctx, nil, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка очистки:", err)
			return 2
		}
		report += "\n" + formatResult(res)
		fmt.Println(formatResult(res))
		if res.Failed > 0 {
			writeReport(opts.report, report)
			return 4
		}
	}
	writeReport(opts.report, report)
	return 0
}

func writeReport(path, text string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o640); err != nil {
		fmt.Fprintln(os.Stderr, "не удалось сохранить отчёт:", err)
		return
	}
	fmt.Println("отчёт сохранён:", path)
}

// formatReport печатает план очистки в виде текстовой таблицы.
func formatReport(plan *cleaner.Plan) string {
	items := append([]cleaner.Item(nil), plan.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Action != items[j].Action {
			return items[i].Action > items[j].Action
		}
		if items[i].GroupTitle != items[j].GroupTitle {
			return items[i].GroupTitle < items[j].GroupTitle
		}
		return items[i].Rank < items[j].Rank
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%s, %s\n", buildinfo.ProductName, buildinfo.FullVersion())
	fmt.Fprintf(&b, "Правило: %s\n", plan.Policy)
	fmt.Fprintf(&b, "Всего сеансов: %d | пользователей: %d | дубликатов: %d | к завершению: %d | служебных: %d\n\n",
		plan.Summary.Total, plan.Summary.Users, plan.Summary.Duplicates, plan.Summary.ToTerminate, plan.Summary.Service)
	fmt.Fprintf(&b, "%-10s %-22s %-18s %-16s %-10s %s\n",
		"РЕШЕНИЕ", "ПОЛЬЗОВАТЕЛЬ", "БАЗА", "КОМПЬЮТЕР", "ПРОСТОЙ", "ПОЯСНЕНИЕ")
	fmt.Fprintln(&b, strings.Repeat("-", 140))
	for _, it := range items {
		fmt.Fprintf(&b, "%-10s %-22s %-18s %-16s %-10s %s\n",
			actionLabel(it.Action), truncate(it.UserName, 22), truncate(it.InfobaseName, 18),
			truncate(it.Host, 16), idleLabel(it.IdleMin), truncate(it.Reason, 60))
	}
	for _, e := range plan.Errors {
		fmt.Fprintf(&b, "\nпредупреждение: %s\n", e)
	}
	return b.String()
}

func formatResult(res *app.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nРезультат: %s\n", res.Message)
	for _, it := range res.Items {
		fmt.Fprintf(&b, "  %-10s %-22s %-18s %s\n",
			statusLabel(it.Status), truncate(it.User, 22), truncate(it.Infobase, 18), it.Detail)
	}
	return b.String()
}

func actionLabel(a string) string {
	switch a {
	case cleaner.ActionTerminate:
		return "ЗАВЕРШИТЬ"
	case cleaner.ActionSkip:
		return "НЕ ТРОГАТЬ"
	}
	return "ОСТАВИТЬ"
}

func statusLabel(s string) string {
	switch s {
	case "terminated":
		return "ЗАВЕРШЁН"
	case "failed":
		return "ОШИБКА"
	}
	return "ПРОПУЩЕН"
}

func idleLabel(m int) string {
	if m < 0 {
		return "—"
	}
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	return fmt.Sprintf("%d ч %d мин", m/60, m%60)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
