#!/usr/bin/env python3
"""Rebuild the synthetic SQLite v9 fixture from an explicit historical source closure.

Run from any directory with cached Go dependencies; never runs a server or loads config.
The generated fixture is a fresh v9 baseline, not every pre-v9 deployment's retained tables.
"""
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tempfile

COMMIT = "75379d7cfa87e5711471b07d3ae311584d477df4"
MODULE = "github.com/XManCodeing/XMall"
ROOT = Path(__file__).resolve().parents[5]
OUTPUT = Path(__file__).with_name("v9_sqlite.sql")
# Only these non-domain/contract packages may be traversed. No configuration,
# bootstrap server, transport, application, credentials or environment files.
EXTRA = {
    "internal/bootstrap/database/migrations", "internal/constants",
    "internal/platform/database/gormdb", "internal/persistence/gormutil",
    "internal/modules/reseller/infrastructure/gormstore",
    "internal/modules/settings/infrastructure/gormstore",
    "internal/shared/money", "internal/shared/jsonmap", "internal/shared/jsonslice",
}


def git(*args):
    """Return a checked Git command's UTF-8 output for this repository only."""
    return subprocess.check_output(["git", "-C", str(ROOT), *args], text=True)


def allowed(package):
    """Return whether a package is in the explicit schema-source allowlist."""
    return package in EXTRA or bool(re.fullmatch(r"internal/modules/[a-z/]+/(domain|contract)", package))


def collect():
    """Return historical source bytes after validating every recursive local import."""
    pending = ["internal/bootstrap/database/migrations"]
    visited, sources = set(), {}
    while pending:
        package = pending.pop()
        if package in visited:
            continue
        if not allowed(package):
            raise RuntimeError("unreviewed historical dependency: " + package)
        visited.add(package)
        paths = git("ls-tree", "--name-only", COMMIT + ":" + package).splitlines()
        for name in paths:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            if package == "internal/modules/reseller/infrastructure/gormstore" and name != "store.go":
                continue
            if package == "internal/modules/orderrisk/contract" and name != "types.go":
                continue
            path = package + "/" + name
            source = git("show", COMMIT + ":" + path)
            if package == "internal/modules/orderrisk/contract":
                source = 'package contract\nimport ("net"; "strings")\n' + source[source.index("// NormalizeRiskIP"):]
            if package == "internal/modules/reseller/infrastructure/gormstore":
                # Exact historical migration declarations only; omit unrelated store
                # transaction methods whose contract imports the config package.
                source = source[:source.index("// BindTx returns")]
                source = re.sub(r'\n\s*resellercontract "[^\"]+"', '', source)
            if re.search(r"func\s+init\s*\(", source):
                raise RuntimeError("historical init needs manual review: " + path)
            sources[path] = source
            for imported in re.findall(r'"(' + re.escape(MODULE) + r'/[^"\s]+)"', source):
                dependency = imported[len(MODULE) + 1:]
                if not allowed(dependency):
                    raise RuntimeError("unreviewed dependency " + dependency + " imported by " + path)
                pending.append(dependency)
    return sources


# Only the historical migration entry is called with an explicitly opened temporary
# SQLite database. No historical InitDB, configuration loader or server is invoked.
RUNNER = r'''package main
import (
 "fmt"
 "os"
 "strings"
 "github.com/XManCodeing/XMall/internal/bootstrap/database/migrations"
 "github.com/XManCodeing/XMall/internal/platform/database/gormdb"
 "github.com/glebarez/sqlite"
 "gorm.io/gorm"
 "gorm.io/gorm/logger"
)
// main builds a new synthetic database and emits its complete schema and rows.
func main() { if err := generate(); err != nil { panic(err) } }
// generate returns any migration/export failure; the database never leaves the temp directory.
func generate() error {
 db, err := gorm.Open(sqlite.Open("synthetic.sqlite"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
 if err != nil { return err }; pool, err := db.DB(); if err != nil { return err }; defer pool.Close()
 pool.SetMaxOpenConns(1); gormdb.DB = db
 if err := migrations.AutoMigrate(); err != nil { return err }
 // Stabilize migration metadata; every row in this database is newly generated.
 if err := db.Exec("UPDATE schema_migrations SET completed_at = 'synthetic-v9-baseline'").Error; err != nil { return err }
 if err := db.Exec("UPDATE settings SET value_json = json_set(value_json, '$.migrated_at', '2026-10-30T00:00:00Z')").Error; err != nil { return err }
 var objects []struct { Type, Name, SQL string }
 if err := db.Raw("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, name").Scan(&objects).Error; err != nil { return err }
 output, err := os.Create("export.sql"); if err != nil { return err }; defer output.Close()
 for _, object := range objects {
  // Preserve historical SQL verbatim, including the multiline migration ledger.
  if _, err := fmt.Fprintln(output, object.SQL + ";"); err != nil { return err }
 }
 for _, object := range objects { if object.Type == "table" { if err := exportRows(db, output, object.Name); err != nil { return err } } }
 return nil
}
// exportRows emits SQLite-quoted values for all synthetic rows, without reading external data.
func exportRows(db *gorm.DB, output *os.File, table string) error {
 var columns []struct { Name string }
 if err := db.Raw("PRAGMA table_info(" + quote(table) + ")").Scan(&columns).Error; err != nil { return err }
 names, values := []string{}, []string{}
 for _, column := range columns { names = append(names, quote(column.Name)); values = append(values, "quote(" + quote(column.Name) + ")") }
 query := "SELECT " + strings.Join(values, ",") + " FROM " + quote(table) + " ORDER BY " + strings.Join(names, ",")
 rows, err := db.Raw(query).Rows(); if err != nil { return err }; defer rows.Close()
 for rows.Next() {
  cells := make([]string, len(columns)); destinations := make([]any, len(columns))
  for index := range cells { destinations[index] = &cells[index] }
  if err := rows.Scan(destinations...); err != nil { return err }
  statement := "INSERT INTO " + quote(table) + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(cells, ",") + ");"
  if _, err := fmt.Fprintln(output, statement); err != nil { return err }
 }
 return rows.Err()
}
// quote returns a double-quoted SQLite identifier from migration-owned metadata.
func quote(identifier string) string { return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\"" }
'''


def canonical_sql(sql):
    """Sort only trailing named foreign keys; GORM iterates their Go map randomly."""
    lines = []
    for line in sql.splitlines(keepends=True):
        if line.startswith("CREATE TABLE") and ",CONSTRAINT `" in line:
            prefix, constraints = line.split(",CONSTRAINT `", 1)
            if not constraints.endswith(");\n"):
                raise RuntimeError("unreviewed foreign-key DDL shape")
            clauses = constraints[:-3].split(",CONSTRAINT `")
            if any(" FOREIGN KEY " not in clause for clause in clauses):
                raise RuntimeError("unreviewed named constraint")
            line = prefix + ",CONSTRAINT `" + ",CONSTRAINT `".join(sorted(clauses)) + ");\n"
        lines.append(line)
    return "".join(lines)


def main():
    """Generate the fixture offline, retaining source hashes for reproducibility."""
    sources = collect()
    print("Reviewed closure:", len(sources), "historical Go files", flush=True)
    if os.environ.get("XMALL_V9_INSPECT_ONLY") == "1":
        print("\n".join(sorted(sources)))
        return
    tool = os.environ.get("XMALL_V9_GO", "go")
    with tempfile.TemporaryDirectory(prefix="xmall-v9-schema-") as temporary:
        work = Path(temporary)
        for path, source in sources.items():
            destination = work / path
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(source)
        for name in ("go.mod", "go.sum"):
            (work / name).write_text(git("show", COMMIT + ":" + name))
        (work / "fixture.go").write_text(RUNNER)
        environment = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOWORK="off")
        result = subprocess.run([tool, "run", "-mod=readonly", "fixture.go"], cwd=work, env=environment, text=True, capture_output=True)
        if result.returncode:
            raise RuntimeError(result.stdout + result.stderr)
        header = ["-- Synthetic fresh v9 SQLite baseline; NOT a production dump or all pre-v9 upgrade histories.", "-- Source commit: " + COMMIT,
                  "-- Named foreign keys sorted; synthetic migration timestamps normalized; data rows ordered.",
                  "-- Source hashes describe compiled slices: reseller migration only; orderrisk NormalizeRiskIP only."]
        for path, source in sorted(sources.items()):
            header.append("-- sha256 " + hashlib.sha256(source.encode()).hexdigest() + " " + path)
        sql = canonical_sql((work / "export.sql").read_text())
        OUTPUT.write_text("\n".join(header) + "\n" + sql)
        print("Fixture SHA256:", hashlib.sha256(OUTPUT.read_bytes()).hexdigest())
        print("Fixture lines:", len(OUTPUT.read_text().splitlines()))


if __name__ == "__main__":
    main()
