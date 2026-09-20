// Package calibration holds the labelled corpus jevgrep's defaults were chosen
// against, and the sweep that turns scores into the numbers behind them.
//
// The corpus is data, not a test: it is compiled into every build so that the
// offline tests next to it can check it stays balanced and well formed, while
// the run that actually scores it against the live model is opt-in and lives
// behind the "calibration" build tag (see online_test.go and `make calibrate`).
// Nothing in `make check` talks to the network.
//
// Two things the corpus is deliberately built to measure, because they are the
// two ways a default threshold goes wrong:
//
//   - Cross-language retrieval. Two cases in three ask a meaning written in one
//     language of a line written in another, because that is the property
//     that separates jevgrep from grep and the one a threshold tuned on English
//     alone would quietly break.
//   - The band around the threshold. Cases marked Boundary are topically
//     adjacent to their meaning without being it (or the reverse: a match that
//     never says the word). They are where a threshold is actually decided;
//     the obvious cases agree at any threshold and decide nothing.
//
// It is small on purpose. Every line scored is a line billed (§2), and a
// corpus large enough to be a benchmark would be a corpus nobody re-runs after
// a model update -- which is the one moment §11 says this has to be re-run.
//
// And one thing it cannot do, which matters more than anything it can: it has
// about as many matches as non-matches, while a real search is almost all
// non-matches. Precision on a balanced corpus therefore reads far better than
// precision on a repository, and its F1 systematically favours a lower
// threshold than real input can live with. That is why the online run also
// scores a few hundred unlabelled lines of ordinary source and reports what
// share of them a threshold would print. Neither half decides a default on
// its own.
package calibration

// Case is one line, one meaning, and whether the line means it. Match is the
// human label: what a user running `jevgrep MEANING` on this line would call a
// correct answer.
type Case struct {
	// Lang is the language of Text: "en", "ja" or "zh". The meaning's own
	// language is not recorded separately -- MeaningLang derives it, and the
	// pair is what makes a case cross-language.
	Lang string
	Text string
	// Meaning is the query, verbatim as it would be typed on the command line.
	Meaning string
	Match   bool
	// Boundary marks a case that sits near the decision, not at either end.
	// These are the cases a threshold is picked by; a corpus of easy cases
	// produces a confident number that means nothing.
	Boundary bool
	// Note says why this case is labelled the way it is, for the next person
	// who disagrees with a label. The sweep never prints it; the run that
	// lists the cases a threshold got wrong does, which is where someone is
	// about to disagree.
	Note string
}

// Cross reports whether the meaning and the line are in different languages,
// which is the retrieval jevgrep exists for and plain grep cannot do at all.
func (c Case) Cross() bool { return c.Lang != MeaningLang(c.Meaning) }

// The meanings, named so that a case and the offline tests refer to the same
// string. Their own languages are mixed on purpose: a user searches in the
// language they think in, not in the language of the logs.
const (
	meaningDiskWrite   = "a disk write failed"
	meaningDiskSpace   = "ディスクの空き容量が足りない"
	meaningAuthFailed  = "用户认证失败"
	meaningOutOfMemory = "the process ran out of memory"
	meaningTimeout     = "ネットワーク接続がタイムアウトした"
	meaningRetry       = "代码里对失败的请求进行了重试"
	meaningCredential  = "a password or API key appears in this line"
	meaningShutdown    = "サーバーが正常に停止した"
)

// MeaningLang is the language a meaning is written in. It is derived rather
// than stored because a meaning is a string a user typed: nothing on the
// command line tells jevgrep what language it is in either.
func MeaningLang(meaning string) string {
	switch meaning {
	case meaningDiskSpace, meaningTimeout, meaningShutdown:
		return "ja"
	case meaningAuthFailed, meaningRetry:
		return "zh"
	default:
		return "en"
	}
}

// cases is the corpus. It is written out in full rather than generated: a
// label is a judgement about one line, and a generator would produce cases
// nobody has read.
var cases = []Case{
	// --- "a disk write failed" (EN meaning) -------------------------------
	{Lang: "en", Match: true, Meaning: meaningDiskWrite,
		Text: `ERROR write to /dev/sda1 failed: no space left on device`},
	{Lang: "ja", Match: true, Meaning: meaningDiskWrite,
		Text: `エラー: ディスクへの書き込みに失敗しました (デバイスの空き容量がありません)`,
		Note: "cross-language: the English meaning has to reach a Japanese log line"},
	{Lang: "zh", Match: true, Meaning: meaningDiskWrite,
		Text: `错误：写入磁盘失败，设备上已没有剩余空间`,
		Note: "cross-language: same line in Chinese"},
	{Lang: "en", Match: false, Meaning: meaningDiskWrite,
		Text: `INFO warmed the page cache in 42ms`},
	{Lang: "ja", Match: false, Meaning: meaningDiskWrite,
		Text: `情報: 設定ファイルを再読み込みしました`},
	{Lang: "zh", Match: false, Meaning: meaningDiskWrite,
		Text: `信息：用户登录成功，已创建会话`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningDiskWrite,
		Text: `WARN disk usage on /var is at 78%`,
		Note: "about the disk, and worrying, but nothing failed to be written"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningDiskWrite,
		Text: `fsync(2) returned EIO for segment 0007.log, giving up on the flush`,
		Note: "a failed write that never uses the words disk, write or fail"},
	{Lang: "zh", Match: false, Boundary: true, Meaning: meaningDiskWrite,
		Text: `读取磁盘上的索引文件失败：文件已损坏`,
		Note: "a disk error, but a read -- the meaning asks for a write"},

	// --- "ディスクの空き容量が足りない" (JA meaning) ------------------------
	{Lang: "en", Match: true, Meaning: meaningDiskSpace,
		Text: `db: cannot extend tablespace: device out of free space`,
		Note: "cross-language: a Japanese meaning against an English line"},
	{Lang: "zh", Match: true, Meaning: meaningDiskSpace,
		Text: `磁盘剩余空间不足 2%，已停止写入新的日志`},
	{Lang: "ja", Match: true, Meaning: meaningDiskSpace,
		Text: `/var のディスク使用率が 99% に達しました`},
	{Lang: "en", Match: false, Meaning: meaningDiskSpace,
		Text: `listening on 0.0.0.0:8080`},
	{Lang: "ja", Match: false, Meaning: meaningDiskSpace,
		Text: `バックアップを S3 にアップロードしました (1.2 GB)`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningDiskSpace,
		Text: `quota exceeded for user bob: 500 MB of 500 MB used`,
		Note: "out of space, but of an account's quota, not of the disk"},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningDiskSpace,
		Text: `free -h reports 112 MiB of available memory`,
		Note: "running out of the wrong resource -- the classic near miss"},

	// --- "用户认证失败" (ZH meaning) ---------------------------------------
	{Lang: "en", Match: true, Meaning: meaningAuthFailed,
		Text: `auth: invalid credentials for user alice, rejecting request`,
		Note: "cross-language: a Chinese meaning against an English line"},
	{Lang: "ja", Match: true, Meaning: meaningAuthFailed,
		Text: `認証エラー: パスワードが一致しません (user=alice)`},
	{Lang: "zh", Match: true, Meaning: meaningAuthFailed,
		Text: `鉴权失败：令牌已过期，拒绝访问`},
	{Lang: "en", Match: false, Meaning: meaningAuthFailed,
		Text: `auth: issued a session token for user alice`,
		Note: "the same subsystem, the opposite outcome"},
	{Lang: "ja", Match: false, Meaning: meaningAuthFailed,
		Text: `ユーザー alice がログアウトしました`},
	{Lang: "zh", Match: false, Meaning: meaningAuthFailed,
		Text: `数据库连接池已满，正在等待可用连接`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningAuthFailed,
		Text: `403 Forbidden: user alice may not delete /v1/orders/9`,
		Note: "authenticated fine, then failed authorization -- a different failure"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningAuthFailed,
		Text: `login attempt #5 from 10.0.0.9 rejected, account now locked`,
		Note: "a failed login without the word authentication anywhere"},

	// --- "the process ran out of memory" (EN meaning) ---------------------
	{Lang: "en", Match: true, Meaning: meaningOutOfMemory,
		Text: `fatal error: runtime: out of memory`},
	{Lang: "ja", Match: true, Meaning: meaningOutOfMemory,
		Text: `致命的: ヒープを拡張できませんでした (メモリ不足)`},
	{Lang: "zh", Match: true, Meaning: meaningOutOfMemory,
		Text: `进程因内存不足被 OOM Killer 终止 (pid=4412)`},
	{Lang: "en", Match: false, Meaning: meaningOutOfMemory,
		Text: `wrote a heap profile to /tmp/heap.pprof`},
	{Lang: "zh", Match: false, Meaning: meaningOutOfMemory,
		Text: `已为上传分配 4 MiB 的缓冲区`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningOutOfMemory,
		Text: `GC pause 412ms, heap 7.8 GiB of 8.0 GiB`,
		Note: "one allocation away from it, but the process is still running"},
	{Lang: "ja", Match: true, Boundary: true, Meaning: meaningOutOfMemory,
		Text: `bash: fork: リソースを一時的に確保できません`,
		Note: "what exhaustion looks like from the shell, in another language"},

	// --- "ネットワーク接続がタイムアウトした" (JA meaning) -------------------
	{Lang: "en", Match: true, Meaning: meaningTimeout,
		Text: `dial tcp 10.2.0.4:5432: i/o timeout`,
		Note: "cross-language, and the phrasing a Go service actually logs"},
	{Lang: "zh", Match: true, Meaning: meaningTimeout,
		Text: `请求上游服务超时（3000ms），已放弃`},
	{Lang: "ja", Match: true, Meaning: meaningTimeout,
		Text: `接続がタイムアウトしました: api.example.com:443`},
	{Lang: "en", Match: false, Meaning: meaningTimeout,
		Text: `GET /healthz 200 in 3ms`},
	{Lang: "zh", Match: false, Meaning: meaningTimeout,
		Text: `与上游服务的连接已建立`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningTimeout,
		Text: `context deadline exceeded while waiting for the lock`,
		Note: "a timeout, but on a local lock: nothing on the network timed out"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningTimeout,
		Text: `upstream took too long to answer, closing the connection`,
		Note: "the same event in plain words, no timeout token to match on"},

	// --- "代码里对失败的请求进行了重试" (ZH meaning, source lines) ----------
	{Lang: "en", Match: true, Meaning: meaningRetry,
		Text: `	for attempt := 1; attempt <= maxAttempts; attempt++ {`,
		Note: "code, not prose: this is what -r over a repository actually reads"},
	{Lang: "en", Match: true, Meaning: meaningRetry,
		Text: `        time.sleep(backoff); resp = session.post(url, json=body)  # try again`},
	{Lang: "en", Match: false, Meaning: meaningRetry,
		Text: `	resp, err := http.Get(url)`},
	{Lang: "en", Match: false, Meaning: meaningRetry,
		Text: `// Package output writes what was found: lines, context, counts, JSON.`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningRetry,
		Text: `	if err != nil { return fmt.Errorf("request failed: %w", err) }`,
		Note: "a failed request handled by giving up, which is the opposite"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningRetry,
		Text: `	backoff := min(base<<attempt, maxBackoff)`,
		Note: "only a retry makes sense of this line, and it never says so"},

	// --- "a password or API key appears in this line" (EN meaning) --------
	// The one meaning here that is about jevgrep's own privacy floor (§6):
	// if the model cannot tell a credential from a column called password,
	// nothing built on top of it can either.
	{Lang: "en", Match: true, Meaning: meaningCredential,
		Text: `PGPASSWORD=hunter2 psql -h db.internal -U app`},
	{Lang: "en", Match: true, Meaning: meaningCredential,
		Text: `curl -H "Authorization: Bearer sk-live-7d2f9a4b1c" https://api.example.com/v1/me`},
	{Lang: "zh", Match: true, Meaning: meaningCredential,
		Text: `# 线上数据库密码：Passw0rd!2024，请勿外传`},
	{Lang: "en", Match: false, Meaning: meaningCredential,
		Text: `ALTER TABLE users ADD COLUMN password_updated_at timestamptz;`,
		Note: "says password, holds none -- the false positive that matters most"},
	{Lang: "ja", Match: false, Meaning: meaningCredential,
		Text: `パスワードの有効期限は 90 日です`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningCredential,
		Text: `export TYPESAFE_API_KEY=$(pass show typesafe/api)`,
		Note: "names a key and reads one, but no secret is in the text"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningCredential,
		Text: `-----BEGIN OPENSSH PRIVATE KEY-----`,
		Note: "unmistakably a credential to a person, and one line of a block"},

	// --- "サーバーが正常に停止した" (JA meaning) ---------------------------
	// A meaning whose matches are the boring lines, to keep the corpus from
	// equating "matches" with "is an error".
	{Lang: "en", Match: true, Meaning: meaningShutdown,
		Text: `server: graceful shutdown complete, 0 connections left`},
	{Lang: "zh", Match: true, Meaning: meaningShutdown,
		Text: `服务已正常关闭，所有连接均已处理完毕`},
	{Lang: "ja", Match: true, Meaning: meaningShutdown,
		Text: `シャットダウン処理が完了しました`},
	{Lang: "en", Match: false, Meaning: meaningShutdown,
		Text: `panic: send on closed channel`},
	{Lang: "ja", Match: false, Meaning: meaningShutdown,
		Text: `サーバーを起動しました (pid=8123)`},
	{Lang: "en", Match: false, Boundary: true, Meaning: meaningShutdown,
		Text: `received SIGKILL, exiting immediately`,
		Note: "stopped, but the opposite of gracefully"},
	{Lang: "en", Match: true, Boundary: true, Meaning: meaningShutdown,
		Text: `all workers drained, exit status 0`,
		Note: "a clean stop described only by its outcome"},
}

// Cases returns the corpus. The slice is a copy: a caller that sorts or
// shuffles it -- the jitter run does -- must not reorder the corpus itself.
func Cases() []Case {
	out := make([]Case, len(cases))
	copy(out, cases)
	return out
}
