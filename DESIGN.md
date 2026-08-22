# Griglia TUI — piano di prodotto e implementazione

Stato: proposta per approvazione, 22 agosto 2026. Questo documento non avvia
l'implementazione.

## 1. Interpretazione del prodotto

Griglia TUI è una todo list locale, terminal-first e transazionale, condivisa da
una persona e da processi agentici indipendenti. Riprende l'esperienza orientata
alle task del package Laravel Griglia, adattandola alle convenzioni e ai vincoli
del terminale. Il binario `griglia` offre due
interfacce sullo stesso application core:

- una TUI pensata per esplorazione, editing e risposte umane;
- comandi non interattivi deterministici, con JSON stabile, pensati anche per
  automazione.

L'interfaccia primaria è una lista di task, non una board Kanban: lifecycle e
stato operativo servono a ordinare, filtrare e rendere riconoscibili le task, ma
non determinano colonne. Il valore specifico è inoltre il protocollo locale
affidabile: scoprire lavoro eseguibile, acquisirlo senza race, riportare
avanzamento e fermare il flusso quando serve una decisione umana.

Griglia non è un task runner, un supervisore di processi agentici, un SDK LLM,
un sistema distribuito o un sostituto di Git/GitHub. Non lancia agenti, non
interpreta i loro messaggi e non presume vendor noti. Non dipende dal progetto
Laravel `alle80/griglia` né ne riusa schema o runtime.

Naming definitivo:

- prodotto/ecosistema: **Griglia**;
- questa implementazione: **Griglia TUI**;
- repository e modulo: `alle80/griglia-tui` e
  `github.com/alle80/griglia-tui`;
- eseguibile, directory dati e metadati: `griglia`.

## 2. Revisione critica delle assunzioni

### Stato non equivale a disponibilità

`BACKLOG → READY → WORKING → DONE` mescola intenzione umana, eseguibilità e
possesso. Una task può essere pianificata come pronta ma avere dipendenze non
risolte; può essere in lavorazione e attendere una risposta. Il v1 salva solo il
lifecycle intenzionale (`backlog`, `ready`, `done`, `cancelled`) e calcola lo
stato operativo:

- `available`: ready, non claimed, dipendenze soddisfatte, nessuna domanda
  bloccante aperta;
- `blocked`: dipendenza non completata;
- `working`: ready con claim attivo e nessuna domanda bloccante aperta;
- `waiting_for_human`: ready con claim attivo e domanda bloccante aperta.

La derivazione ha precedenza deterministica: `done` e `cancelled` riflettono il
lifecycle terminale; per `ready`, una domanda bloccante non risposta con claim
produce `waiting_for_human`, un claim produce `working`, dipendenze insoddisfatte
producono `blocked`, altrimenti lo stato è `available`. `backlog` non ha stato
esecutivo ed è escluso da `claim-next`.

Il claim è così l'unica fonte autorevole del possesso: la combinazione invalida
`working` senza claim non è rappresentabile. Le query di eleggibilità devono già
controllare claim, dipendenze e domande, quindi persistere `working` non le rende
più semplici. TUI e futuri transport ricevono lifecycle e stato operativo come
campi distinti; un futuro orchestratore reagirebbe agli eventi di claim, non a
un secondo flag duplicato. La cronologia non perde informazione perché
`claim_acquired`, `claim_released` e `task_completed` sono eventi append-only.

`failed` è un esito di un tentativo, non necessariamente lo stato della task.
Nel v1 `task release --reason ...` chiude il claim e conserva il motivo come
evento/commento; il lifecycle resta `ready`, quindi la task torna disponibile se
non è bloccata. Un modello formale di tentativi arriverà solo quando esistono
casi reali. `cancelled` è invece terminale e utile subito.

### La percentuale è informativa

Il progresso è una stima, quindi l'unico vincolo è 0–100: può aumentare o
diminuire senza flag speciali quando emerge nuovo lavoro. Non guida transizioni
automatiche: 100% non significa `done`, mentre `done` normalizza a 100. Ogni
aggiornamento resta nell'event log, che rende visibili anche le regressioni senza
imporre una falsa monotonicità.

### Le domande non sono tutte bloccanti

Una task può avere più domande. `ask` crea per default una domanda bloccante;
`--non-blocking` permette richieste informative. Risposta e presa visione sono
distinte: `answered_at` segnala la risposta umana, `acknowledged_at` segnala che
l'agente l'ha consumata. Una domanda bloccante smette di bloccare quando è
risposta, non quando è acknowledged. In v1 una sola risposta canonica è
modificabile fino all'acknowledgement; dopo, una correzione crea una nuova
domanda/risposta per non cambiare input già consumato.

### `next` non assegna lavoro

`task next` è solo una lettura e può restituire la stessa task a più processi.
L'operazione corretta per un worker è `task claim-next`, che seleziona e crea il
claim nella stessa transazione. `claim ID` resta utile quando la task è stata
scelta esplicitamente. Un conflitto produce un errore tipizzato, mai un successo
ambiguo.

### Niente lease automatico nel primo rilascio

Un semplice nome agente non distingue due istanze. Il v1 accetta `--agent`
obbligatorio e `--instance` opzionale; se assente genera o legge un identificatore
di sessione dal chiamante, senza catalogo vendor. Il claim non scade da solo:
sleep, debugging o sospensione del laptop rendono pericolose le lease. Sono
previsti `last_activity_at`, `task release` e `task reclaim --force` esplicito.
Heartbeat e TTL potranno essere aggiunti dopo aver definito semantiche robuste.

### Piani e audit log

Le dipendenze a grafo aciclico bastano per il v1; una tabella `plans` sarebbe un
contenitore senza comportamento verificato. È invece utile da subito un event
log append-only: rende diagnosticabili claim, transizioni, domande e modifiche,
senza tentare event sourcing.

### Limiti locali da dichiarare

SQLite in WAL è adatto a processi concorrenti sulla stessa macchina, ma WAL usa
shared memory e non è indicato su filesystem di rete. Griglia deve rifiutare o
almeno avvertire chiaramente per path noti come remoti, documentare che database,
`-wal` e `-shm` sono un'unità, impostare `busy_timeout` e non promettere
collaborazione multi-host.

## 3. Valutazione tecnologica

Le versioni esatte saranno fissate nel `go.mod` al primo milestone e aggiornate
solo con test. La valutazione corrente è:

| Responsabilità | Scelta proposta | Motivo e alternative |
|---|---|---|
| TUI | Bubble Tea v2 + Bubbles v2 + Lip Gloss v2 | Stack coerente e ormai stabile; v2 rende view e capability terminali dichiarative. Usare Bubbles solo per input, viewport, help e progress, non come framework applicativo. [Release Bubble Tea](https://github.com/charmbracelet/bubbletea/releases), [upgrade Bubbles v2](https://github.com/charmbracelet/bubbles/blob/main/UPGRADE_GUIDE_V2.md). |
| CLI | Cobra v1 | Gerarchia, help, completion ed error handling maturi. `flag` richiederebbe un dispatcher artigianale; Kong è più compatto ma lega il contratto a tag/struct e offre meno controllo esplicito sull'output. [Cobra](https://github.com/spf13/cobra). |
| SQLite | `modernc.org/sqlite` tramite `database/sql` | Niente CGO e cross-build di un singolo binario. `mattn/go-sqlite3` è molto maturo ma richiede CGO/GCC; `ncruces/go-sqlite3` è una valida alternativa cgo-free con sandbox Wasm per connessione, da benchmarkare prima di cambiare. [modernc](https://pkg.go.dev/modernc.org/sqlite), [mattn](https://github.com/mattn/go-sqlite3), [ncruces](https://github.com/ncruces/go-sqlite3). |
| Migrazioni | file SQL numerati in `embed.FS`, runner interno minimale | Per un solo database e migrazioni forward-only, `goose` aggiunge più superficie del necessario. Adottarlo se emergono migrazioni Go o tooling esterno. [goose](https://github.com/pressly/goose). |
| JSON | `encoding/json` | Envelope e DTO espliciti, nessuna dipendenza. Non serializzare direttamente entità domain. |
| Config | nessuna libreria nel v1 | Flag, env mirate e discovery del progetto bastano. Se servirà un file, TOML con decoder piccolo; evitare Viper e precedenze implicite. |
| Logging | `log/slog` verso stderr o file | Standard library, strutturato. In modalità `--json`, stdout contiene esclusivamente la risposta del comando. [slog](https://pkg.go.dev/log/slog). |
| Test | `testing`, `httptest` non necessario, `go test -race`, golden file piccoli | Aggiungere `testify` solo se riduce davvero rumore; niente mock framework. |

La baseline Go raccomandata è l'ultima major stabile supportata dai tre OS target
al momento dell'implementazione, dichiarando una policy (ultima e penultima),
non inseguendo una versione solo per novità.

## 4. Modello di dominio minimo

### Entità

`Project` contiene UUID, nome visualizzato e root canonica alla creazione. La
root salvata è informativa: il database resta spostabile. La versione dello
schema SQLite appartiene esclusivamente al migration runner.

`Task` contiene ID numerico locale, UUID pubblico futuro, lifecycle, priorità,
progress, phase/message, timestamps, completion summary e una versione intera
per optimistic checks. L'ordinamento deterministico è:
priorità discendente, `created_at` ascendente, `id` ascendente.

`Dependency` è un arco `task_id -> depends_on_task_id`. Niente self-edge, niente
duplicati e niente cicli. Una task è disponibile solo se tutte le dipendenze sono
`done`; una dipendenza cancelled non sblocca automaticamente.

`Claim` rappresenta il possesso esclusivo corrente: task, agent name, instance,
timestamp di acquisizione e ultima attività. Una task ha al massimo un claim
attivo. Acquisire non cambia il lifecycle `ready`: rende lo stato operativo
`working`. Rilasciare chiude il claim e lascia il lifecycle `ready`; completare
porta atomicamente il lifecycle a `done` e chiude il claim.

`Question` contiene testo, blocking, autore agente/istanza, stato temporale e
l'eventuale `Answer` canonica con autore umano. Il v1 non richiede identità o
autenticazione umana locale.

`Comment` è testo append-only con autore tipizzato (`human`, `agent`, `system`)
e nome opzionale. `Event` registra operazione, payload JSON interno e timestamp.

### Invarianti importanti

- titolo non vuoto e con limite documentato; testo UTF-8 valido;
- priority è un enum (`low`, `normal`, `high`, `urgent`), non un numero aperto;
- terminali `done` e `cancelled` non sono claimabili;
- solo `ready` e realmente disponibile è eleggibile da `claim-next`;
- dipendenze e lifecycle non possono essere modificati durante un claim attivo,
  eccetto completamento/cancellazione attraverso use case transazionali;
- progress 0–100, aumenti e diminuzioni sono entrambi validi; aggiornamenti
  richiedono il claim owner, salvo `--force` umano esplicito;
- modifiche operative verificano agent+instance, non solo il nome;
- `done` richiede dipendenze già done, chiude claim e domande aperte solo con
  una scelta esplicita (default: rifiuta se esistono domande bloccanti);
- ogni mutazione aggiorna `updated_at`, incrementa `version` e aggiunge un evento
  nella stessa transazione;
- timestamp UTC RFC 3339 con precisione microsecondi al confine JSON; rendering
  locale solo nella TUI/output umano.

## 5. Storage e transazioni

Percorso predefinito: `<project>/.griglia/griglia.db`. I comandi cercano
`.griglia/griglia.db` dalla directory corrente verso i genitori; `--project`
seleziona esplicitamente root o database. `griglia init` crea `.griglia/`, il DB
e un `.gitignore` interno che ignora `griglia.db*`: il database è stato locale,
non un artefatto adatto al merge Git. `GRIGLIA_PROJECT` è l'unico override env
iniziale. Config globale, se futura, vivrà nelle directory XDG con application
name `griglia`.

Schema iniziale (tipi abbreviati; tutte le foreign key sono abilitate):

```sql
projects(id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL);
schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);

tasks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  lifecycle TEXT NOT NULL CHECK(lifecycle IN
    ('backlog','ready','done','cancelled')),
  priority TEXT NOT NULL CHECK(priority IN
    ('low','normal','high','urgent')),
  progress INTEGER NOT NULL DEFAULT 0 CHECK(progress BETWEEN 0 AND 100),
  phase TEXT NOT NULL DEFAULT '',
  completion_summary TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  completed_at TEXT, cancelled_at TEXT,
  version INTEGER NOT NULL DEFAULT 1
);

task_dependencies(
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  depends_on_task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(task_id, depends_on_task_id),
  CHECK(task_id <> depends_on_task_id)
);

claims(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id),
  agent_name TEXT NOT NULL, instance_id TEXT NOT NULL,
  claimed_at TEXT NOT NULL, last_activity_at TEXT NOT NULL,
  released_at TEXT, release_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX one_active_claim_per_task
  ON claims(task_id) WHERE released_at IS NULL;

questions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  text TEXT NOT NULL, blocking INTEGER NOT NULL CHECK(blocking IN (0,1)),
  asked_by TEXT NOT NULL, asked_instance TEXT NOT NULL,
  asked_at TEXT NOT NULL, answered_at TEXT, acknowledged_at TEXT
);
answers(
  question_id INTEGER PRIMARY KEY REFERENCES questions(id) ON DELETE CASCADE,
  text TEXT NOT NULL, answered_by TEXT NOT NULL DEFAULT 'human',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

comments(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL
  REFERENCES tasks(id) ON DELETE CASCADE, author_kind TEXT NOT NULL,
  author_name TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, created_at TEXT NOT NULL);
events(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER REFERENCES tasks(id),
  kind TEXT NOT NULL, actor_kind TEXT NOT NULL, actor_name TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL);
```

Indici aggiuntivi coprono l'ordinamento delle task, dipendenze inverse, domande
aperte ed eventi per task. I `CHECK` sono difesa storage, non sostituti delle
regole domain.

All'apertura: `foreign_keys=ON`, `journal_mode=WAL`, `busy_timeout` finito (per
esempio 5 s), `synchronous=NORMAL`; migrazioni sotto lock di scrittura. WAL
consente lettori e un writer concorrente, ma sempre un solo writer alla volta:
[documentazione SQLite WAL](https://www.sqlite.org/wal.html).

`claim-next` apre una write transaction immediata, seleziona la prima task
`ready` eleggibile con query anti-join su claim/dipendenze/domande, inserisce il
claim e l'evento, poi commit; non aggiorna il lifecycle. Il partial unique index
è la seconda linea di difesa. Un busy timeout o unique conflict viene tradotto
in errore stabile; il comando può ritentare un numero piccolo e limitato con
jitter, rifacendo la selezione. Non si usa `next` seguito da `claim`
internamente.

Answer+question timestamp, progress+claim activity+event, done+claim release+
event sono ciascuno una singola transazione. Il controllo dei cicli avviene con
CTE ricorsiva nella stessa write transaction che inserisce l'arco.

Le migrazioni SQL sono embedded, checksumate e applicate in ordine. Il runner
rifiuta una versione DB più nuova del binario e una migration già applicata con
checksum diverso. Backup e downgrade automatici sono fuori v1; prima di ogni
migrazione non banale il comando crea un backup coerente esplicito.

## 6. Contratto CLI

```text
griglia                         apre la TUI
griglia init [--name NAME]
griglia version [--json]

griglia task add TITLE [--description ...] [--priority ...] [--lifecycle ...]
griglia task list [--lifecycle ...] [--state ...] [--available] [--json]
griglia task show ID [--json]
griglia task edit ID ...
griglia task ready ID
griglia task cancel ID [--reason ...]
griglia task next [--json]
griglia task claim ID --agent NAME [--instance ID] [--json]
griglia task claim-next --agent NAME [--instance ID] [--json]
griglia task progress ID PERCENT --agent NAME --instance ID [--message ...]
griglia task ask ID TEXT --agent NAME --instance ID [--non-blocking] [--json]
griglia task questions ID [--unacknowledged] [--json]
griglia task acknowledge ID QUESTION --agent NAME --instance ID [--json]
griglia task release ID --agent NAME --instance ID [--reason ...] [--json]
griglia task done ID --agent NAME --instance ID [--comment ...] [--json]
griglia task depend ID --on OTHER_ID

griglia question list [--open] [--json]
griglia question answer QUESTION_ID TEXT [--json]
griglia agent list [--active] [--json]
```

Non propongo `agent-api` nel v1: duplicherebbe comandi e documentazione senza
creare isolamento reale. La compatibilità è definita dall'output `--json`, non
dal tipo di chiamante. Se un futuro server/MCP richiede una API differente, si
aggiungerà un namespace versionato allora.

Ogni JSON usa un envelope, anche per gli errori:

```json
{"protocol_version":"1","ok":true,"data":{"task":{}},"error":null}
```

`protocol_version` versiona esclusivamente il protocollo machine-readable e non
è collegato alla versione delle migrazioni SQLite. Campi additivi sono permessi
nella stessa versione; rimozioni, rename o cambio di semantica richiedono una
nuova major di protocollo. Liste sono sempre array,
valori assenti sono coerentemente `null` o omessi secondo DTO documentati, ID
sono numeri nel protocollo v1 e gli enum sono lowercase. Nessun colore, prompt,
spinner o log su stdout con `--json`.

I DTO task espongono separatamente `lifecycle` persistito e
`operational_state` derivato; non espongono un ambiguo campo `status`.

Exit code: 0 successo; 2 uso/input non valido; 3 progetto non inizializzato; 4
not found; 5 conflict (claim/stato/versione); 6 temporaneamente busy; 1 errore
interno. In JSON anche gli errori emettono un solo envelope su stdout; stderr è
riservato a diagnostica opt-in. I comandi mutanti non chiedono conferme se
`--json`; l'output umano può farlo solo per operazioni distruttive esplicite.

## 7. Architettura TUI

Esiste un solo root `Model`, con route (`list`, `detail`, `questions`, `form`,
`help`), dimensioni terminale, focus, keymap, theme e stato dell'operazione.
Le schermate sono submodel value-type; overlay e form sono stati espliciti, non
programmi Bubble Tea annidati.

Flusso:

```text
key/window message -> root Update -> screen Update -> application Cmd async
application result message -> root -> aggiorna snapshot/errore -> View pura
```

I `tea.Cmd` chiamano servizi applicativi e restituiscono messaggi tipizzati
(`tasksLoadedMsg`, `taskSavedMsg`, `questionAnsweredMsg`, `operationFailedMsg`).
Nessuna goroutine tocca direttamente il model. Il repository non entra nei
submodel e la TUI non costruisce SQL o decide transizioni.

La schermata primaria è una lista di task keyboard-first. Deve permettere di
capire rapidamente cosa resta da fare, cosa è disponibile per un agente, cosa è
in lavorazione, cosa attende una decisione umana e cosa è stato completato di
recente. Lifecycle (`backlog`, `ready`, `done`, `cancelled`) e stato operativo
derivato (`available`, `working`, `blocked`, `waiting_for_human`) sono metadati
per ordinamento, filtri e indicatori: non definiscono gruppi o colonne della UI.
Stati diversi restano distinguibili anche senza colore, usando una combinazione
coerente di simboli, testo, tipografia e spaziatura.

La lista può offrire ricerca, filtri per lifecycle, stato operativo e priorità,
ordinamento e visibilità delle task completate. Questi controlli devono restare
progressivi e minimali nel v1. Ogni riga privilegia titolo e stato; quando lo
spazio lo consente aggiunge ID, priorità, agente, progresso e fase. La selezione
può mostrare un'anteprima oppure aprire il dettaglio, che raccoglie descrizione,
lifecycle, stato operativo, ownership, progresso, fase, dipendenze, domande e
commenti senza sovraccaricare la lista.

Il layout si adatta progressivamente alla larghezza, senza fissare ora
breakpoint o una composizione definitiva: su terminali larghi può affiancare un
pannello di anteprima e mostrare più metadati; su quelli medi riduce le colonne
informative e apre il dettaglio separatamente; su quelli stretti privilegia
indicatore e titolo, usa una seconda riga per i metadati essenziali e presenta
il dettaglio a schermo intero. Focus e selezione sono per task ID, non indice,
così un refresh non sposta accidentalmente l'utente. `?` apre help contestuale,
`/` ricerca o filtra, `q` torna indietro e `Q`/Ctrl-C esce. Domande bloccanti
hanno un indicatore non basato solo sul colore. Errori restano visibili e
retryable; empty/loading states sono first-class. Polling leggero del DB aggiorna
la lista mentre agenti esterni lavorano; un refresh manuale resta disponibile.

## 8. Struttura del repository

```text
cmd/griglia/main.go             composizione e process exit
internal/domain/               entità, enum, invarianti, errori
internal/app/                  use case e piccoli port repository/clock
internal/sqlite/               database/sql, query, tx, migrazioni embedded
internal/cli/                  Cobra, DTO JSON, renderer umano, exit mapping
internal/tui/                  root model, schermate, componenti e stile
```

Cinque package applicativi sono sufficienti. Le interfacce vivono nel package
che le consuma (`app`) e sono strette per use case, non un repository universale.
`domain` non importa SQLite, Cobra o Bubble Tea. CLI e TUI dipendono da `app` e
condividono use case, non presenter code. Non serve `pkg/`: non promettiamo una
libreria pubblica nel v1. `cmd/griglia` garantisce il nome del binario mentre il
module path resta quello del repository.

## 9. Strategia di test

- Domain: table test per transizioni di lifecycle, derivazione degli stati
  operativi, progress anche decrescente, terminal states, domande e ordinamento;
  property/fuzz test per grafi di dipendenze e input testuali.
- SQLite: database temporanei reali, migration da zero e da ogni versione,
  foreign key/check/partial index, reopen e query di eleggibilità. Nessun mock
  SQL per ciò che SQLite deve garantire.
- Concorrenza: più processi o più handle DB sincronizzati da una barrier lanciano
  `claim-next`; ogni task viene assegnata al massimo una volta e il lifecycle
  resta `ready`. Eseguire ripetuto, con `-race`, WAL e busy contention; verificare
  anche crash prima/dopo commit e che release renda nuovamente eleggibile la task.
- Application: fake clock e repository minimale per invarianti di use case ed
  error mapping. Il tempo è iniettato, gli ID DB no.
- CLI: esecuzione in-process con buffer separati; golden JSON canonico per
  successi/errori e test di exit code/stdout/stderr. I golden ignorano solo campi
  dinamici sostituiti esplicitamente, non normalizzano tutta la risposta.
- TUI: `Update` deterministico con messaggi sintetici, snapshot selettivi della
  lista e del dettaglio a dimensioni wide/medium/narrow, focus preservation e
  flussi answer/form; verificare che ogni stato sia distinguibile senza colore.
- End-to-end: binary test per `init → add → ready → claim-next → progress → ask →
  answer → acknowledge → done`, più due processi in gara.

CI su Linux, macOS e Windows; unit test ovunque, race test almeno Linux. Testare
build con `CGO_ENABLED=0` è un requisito di distribuzione.

## 10. Roadmap a vertical slice

1. **Scheletro usabile** — modulo corretto, `cmd/griglia`, version, discovery,
   init, migration 001, add/list/show in testo e JSON. Risultato:
   `griglia init && griglia task add "First task" && griglia task list`.
2. **Prima TUI** — lista task read-only responsive, detail, help, error/empty
   states, poi form add. Risultato: `griglia` è già utile per vedere e creare
   task.
3. **Lifecycle** — edit/ready/cancel/done, priorità e audit events; la TUI usa
   gli stessi use case.
4. **Coordinamento agenti** — claim model, `next`, `claim`, `claim-next`, release,
   progress non monotono, ownership checks, agent activity e prove concorrenti
   multiprocesso.
5. **Human-in-the-loop** — ask/list/answer/acknowledge, indicatori nella lista e
   inbox domande nella TUI, blocking derivato.
6. **Dipendenze** — DAG, cycle prevention, disponibilità derivata, UI minima per
   vedere e aggiungere archi.
7. **Hardening v1** — protocol golden suite, completions, docs agent workflow,
   backup/migration checks, packaging cross-platform, benchmark DB/TUI refresh,
   accessibility e release candidate.

Ogni milestone include docs, test e migrazione eventuale; nessun layer viene
costruito in anticipo senza un comando o una schermata che lo eserciti.

## 11. Decisioni aperte da approvare

1. **Database nel progetto.** Raccomandazione: `.griglia/griglia.db`, ignorato da
   Git, discovery verso i parent. Alternativa: XDG con mapping delle root, che
   evita file nel repo ma rende copia/discovery e uso da agenti meno trasparenti.
2. **Lifecycle salvato.** Raccomandazione: backlog/ready/done/cancelled, con
   available/working/blocked/waiting derivati. Alternative: persistere working,
   blocked o failed, più visibile nello storage ma duplicato e facilmente
   incoerente con claim, dipendenze e domande.
3. **Scadenza claim.** Raccomandazione: nessuna scadenza automatica v1, release e
   force-reclaim espliciti. Alternativa: lease+heartbeat, utile per crash ma
   rischiosa durante sospensioni e richiede una policy di recovery.
4. **Driver SQLite.** Raccomandazione: modernc cgo-free, con spike di build,
   dimensione e contention nel milestone 1. Alternative: ncruces (cgo-free,
   sandbox Wasm per connessione) o mattn (maturo/veloce, ma CGO complica release).
5. **Confine API agenti.** Raccomandazione: stessi comandi, envelope JSON v1
   contrattuale. Alternativa: `agent-api`, più evidente ma duplicato e prematuro.
6. **Correzione risposte.** Raccomandazione: edit fino all'ack, poi nuova domanda
   correlata. Alternativa: risposte append-only multiple già nel v1, più auditabile
   ma più complessa da consumare deterministicamente.

Con l'approvazione di queste sei scelte, il milestone 1 può iniziare senza altre
decisioni di prodotto.
