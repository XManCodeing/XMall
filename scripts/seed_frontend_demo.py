#!/usr/bin/env python3
"""Insert isolated TEST catalog fixtures; normal seeding never updates slugs.

Usage: python3 scripts/seed_frontend_demo.py --db /absolute/path/dujiao.db [--dry-run]
Use --repair-json-storage to convert this exact existing fixture batch from TEXT
JSON to BLOB JSON without inserting rows or changing values. Only categories,
products and product_skus are queried or changed. A complete SQLite online
backup is made before every real run, including no-op reruns.
"""

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import random
import sqlite3
import subprocess

# These Go Valuer implementations return []byte, and their Scanners accept
# []byte only. Python strings bind as SQLite TEXT; UTF-8 bytes bind as BLOB.
JSON_COLUMNS = {
    "categories": ("name_json",),
    "products": (
        "seo_meta_json", "title_json", "description_json", "content_json",
        "instructions_json", "wholesale_prices", "images", "tags",
        "manual_form_schema_json",
    ),
    "product_skus": ("spec_values_json",),
}

PREFIX = "demo-orbit-20261025-"
LOCALES = ("zh-CN", "zh-TW", "en-US")
WARNINGS = (
    "TEST 测试商品，无真实服务，请勿真实付款。",
    "TEST 測試商品，無真實服務，請勿真實付款。",
    "TEST item only. No real service. Do not make real payments.",
)
CATEGORIES = (
    ("creative", ("TEST 创意工具", "TEST 創意工具", "TEST Creative Tools")),
    ("productivity", ("TEST 效率办公", "TEST 效率辦公", "TEST Productivity")),
    ("developer", ("TEST 开发工具", "TEST 開發工具", "TEST Developer Tools")),
    ("learning", ("TEST 学习资源", "TEST 學習資源", "TEST Learning Resources")),
)
NAMES = (
    ("灵感画板", "靈感畫板", "Inspiration Canvas"),
    ("图标设计套件", "圖示設計套件", "Icon Design Kit"),
    ("摄影调色预设", "攝影調色預設", "Photo Color Presets"),
    ("动态演示模板", "動態展示範本", "Motion Presentation Templates"),
    ("品牌视觉素材库", "品牌視覺素材庫", "Brand Visual Library"),
    ("专注工作空间", "專注工作空間", "Focus Workspace"),
    ("项目协作看板", "專案協作看板", "Project Collaboration Board"),
    ("会议笔记模板", "會議筆記範本", "Meeting Notes Templates"),
    ("个人知识管理手册", "個人知識管理手冊", "Personal Knowledge Handbook"),
    ("跨设备日程规划工具", "跨裝置日程規劃工具", "Cross-device Schedule Planner"),
    ("接口调试工作台", "介面除錯工作台", "API Debugging Workbench"),
    ("代码片段管理器", "程式碼片段管理器", "Code Snippet Manager"),
    ("终端主题合集", "終端機主題合集", "Terminal Theme Collection"),
    ("数据可视化组件示例", "資料視覺化元件範例", "Data Visualization Examples"),
    ("自动化脚本练习集", "自動化腳本練習集", "Automation Script Exercises"),
    ("语言学习计划", "語言學習計畫", "Language Learning Plan"),
    ("设计基础训练营", "設計基礎訓練營", "Design Fundamentals Workshop"),
    ("编程入门练习册", "程式設計入門練習冊", "Beginner Programming Workbook"),
    ("阅读进度记录模板", "閱讀進度記錄範本", "Reading Progress Templates"),
    ("跨平台团队知识协作与长期学习成长规划完整演示组合：包含多语言长标题换行、边界布局及购买流程测试场景", "跨平台團隊知識協作與長期學習成長規劃完整展示組合：包含多語言長標題換行、邊界版面及購買流程測試情境", "Complete Cross-platform Team Knowledge Collaboration and Long-term Learning Plan Demonstration Bundle with Multilingual Long-title Wrapping, Layout Edge Cases and Checkout Test Scenarios"),
)
MULTI = {1, 2, 6, 7, 11, 16}
SOLD_OUT = {5, 19}
TIERS = (
    ("STANDARD", ("标准", "標準", "Standard")),
    ("PRO", ("专业", "專業", "Professional")),
    ("TEAM", ("团队", "團隊", "Team")),
)

# Active SKU totals are remaining sellable stock, not total minus locked/sold.
VERIFY_SQL = """
SELECT p.id, p.slug
FROM products p
LEFT JOIN product_skus s ON s.product_id=p.id
    AND s.is_active=1 AND s.deleted_at IS NULL
WHERE p.slug GLOB ?
GROUP BY p.id
HAVING COUNT(s.id)=0
    OR p.manual_stock_total != SUM(s.manual_stock_total)
    OR p.manual_stock_locked != SUM(s.manual_stock_locked)
    OR p.manual_stock_sold != SUM(s.manual_stock_sold)
    OR ROUND(p.price_amount,2) != ROUND(MIN(s.price_amount),2)
    OR MIN(s.manual_stock_total)<0
"""


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def localized(values):
    return encoded(dict(zip(LOCALES, values)))


def plan():
    rng = random.Random(20261025)
    categories = [
        {"slug": PREFIX + "category-" + key, "name_json": localized(names),
         "parent_id": 0, "icon": "", "sort_order": 0, "is_active": 1}
        for key, names in CATEGORIES
    ]
    products = []
    for index, names in enumerate(NAMES, 1):
        tiers = TIERS if index in MULTI else (("DEFAULT", None),)
        prices = sorted(rng.sample(range(990, 39901), len(tiers)))
        skus = []
        for position, ((code, tier_names), cents) in enumerate(zip(tiers, prices)):
            skus.append({
                "sku_code": code,
                "spec_values_json": localized(tier_names) if tier_names else "{}",
                "price_amount": f"{cents // 100}.{cents % 100:02d}",
                "cost_price_amount": "0.00",
                "manual_stock_total": 0 if index in SOLD_OUT else rng.randint(8, 45),
                "manual_stock_locked": 0, "manual_stock_sold": 0,
                "is_active": 1, "sort_order": position,
            })
        product = {
            "slug": PREFIX + f"product-{index:02d}",
            "seo_meta_json": "{}",
            "title_json": localized(tuple("[TEST] " + name for name in names)),
            "description_json": localized(WARNINGS),
            "content_json": localized(tuple(
                f"## {name}\n\n{warning}\n\n{detail}"
                for name, warning, detail in zip(names, WARNINGS, (
                    "仅用于前台商品列表、详情、规格及库存展示测试。不会交付真实服务或卡密。",
                    "僅用於前台商品列表、詳情、規格及庫存展示測試。不會交付真實服務或卡密。",
                    "For storefront list, detail, variant and stock display testing only. No real service or activation keys will be delivered.",
                )))),
            "instructions_json": localized(tuple(
                warning + "\n\n" + detail for warning, detail in zip(WARNINGS, (
                    "请仅查看测试页面，不要付款。本商品没有真实交付内容。",
                    "請僅查看測試頁面，不要付款。本商品沒有真實交付內容。",
                    "Inspect the test pages only. Do not pay. This item has no real deliverables.",
                )))),
            "price_amount": skus[0]["price_amount"], "cost_price_amount": "0.00",
            "wholesale_prices": "[]", "images": "[]", "tags": encoded(["TEST", "DEMO"]),
            "purchase_type": "guest" if index <= 12 else "member",
            "min_purchase_quantity": 1, "max_purchase_quantity": 5,
            "stock_display_mode": "exact", "fulfillment_type": "manual",
            "manual_form_schema_json": "{}",
            "manual_stock_total": sum(s["manual_stock_total"] for s in skus),
            "manual_stock_locked": 0, "manual_stock_sold": 0,
            "is_affiliate_enabled": 0, "is_mapped": 0, "is_active": 1, "sort_order": 0,
        }
        products.append((categories[(index - 1) // 5]["slug"], product, skus))
    return categories, products


def insert(conn, table, row):
    row = dict(row)
    for column in JSON_COLUMNS[table]:
        if column in row:
            # json.dumps returns str; bind bytes to match Go json.Marshal.
            row[column] = row[column].encode("utf-8")
    columns = ",".join(row)
    placeholders = ",".join("?" for _ in row)
    return conn.execute(f"INSERT INTO {table} ({columns}) VALUES ({placeholders})", tuple(row.values())).lastrowid


def existing(conn, table, slug):
    return conn.execute(f"SELECT id,deleted_at FROM {table} WHERE slug=?", (slug,)).fetchone()


def counts(conn):
    return {table: conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            for table in ("categories", "products", "product_skus")}


def seed_counts(conn):
    result = {table: conn.execute(f"SELECT COUNT(*) FROM {table} WHERE slug GLOB ?", (PREFIX + "*",)).fetchone()[0]
              for table in ("categories", "products")}
    result["product_skus"] = conn.execute(
        "SELECT COUNT(*) FROM product_skus s JOIN products p ON p.id=s.product_id WHERE p.slug GLOB ?",
        (PREFIX + "*",)).fetchone()[0]
    return result


def repair_json_storage(conn, db, dry_run):
    """Repair only this exact fixture batch; never insert or change JSON values."""
    expected_db = Path(__file__).resolve().parents[1] / "db" / "dujiao.db"
    if db != expected_db.resolve():
        raise RuntimeError("Repair is restricted to this project's db/dujiao.db")
    opened = subprocess.check_output(
        ["/usr/sbin/lsof", "-nP", "-Fpcn", str(db)], text=True,
    )
    if "cserver\n" not in opened or f"n{db}\n" not in opened:
        raise RuntimeError("Running server must have this exact database open")
    print(encoded({"open_database": opened.strip()}))
    categories, products = plan()
    for table, slugs in (
        ("categories", {row["slug"] for row in categories}),
        ("products", {row["slug"] for _, row, _ in products}),
    ):
        actual = {row[0] for row in conn.execute(
            f"SELECT slug FROM {table} WHERE slug GLOB ?", (PREFIX + "*",))}
        if actual != slugs:
            raise RuntimeError(f"Unexpected {table} fixture batch; refusing repair")
    if seed_counts(conn) != {"categories": 4, "products": 20, "product_skus": 32}:
        raise RuntimeError("Unexpected fixture counts; refusing repair")
    filters = {
        "categories": "slug GLOB ?",
        "products": "slug GLOB ?",
        "product_skus": "product_id IN (SELECT id FROM products WHERE slug GLOB ?)",
    }

    def snapshot():
        # Compare all catalog rows, including non-fixtures, while normalizing
        # only the intentional str -> bytes representation change.
        result = {}
        for table, columns in JSON_COLUMNS.items():
            cursor = conn.execute(f"SELECT * FROM {table} ORDER BY id")
            names = [item[0] for item in cursor.description]
            result[table] = [tuple(
                value.decode("utf-8") if names[i] in columns and isinstance(value, bytes) else value
                for i, value in enumerate(row)
            ) for row in cursor]
        return result

    def pending():
        result = {}
        for table, columns in JSON_COLUMNS.items():
            result[table] = {}
            for column in columns:
                rows = conn.execute(
                    f"SELECT {column},typeof({column}) FROM {table} WHERE {filters[table]}",
                    (PREFIX + "*",)).fetchall()
                for value, storage in rows:
                    if storage not in ("text", "blob"):
                        raise RuntimeError(f"Unexpected storage for {table}.{column}: {storage}")
                    json.loads(value)
                result[table][column] = sum(storage == "text" for _, storage in rows)
        return result

    print(encoded({"repair_pending": pending(), "dry_run": dry_run}))
    if dry_run:
        return
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    backup_path = db.with_name(db.name + f".json-repair-{PREFIX.rstrip('-')}-{stamp}.bak")
    backup_path.touch(mode=0o600, exist_ok=False)
    with sqlite3.connect(backup_path) as backup:
        conn.backup(backup)
    print(encoded({"backup": str(backup_path)}))
    conn.execute("BEGIN IMMEDIATE")
    try:
        before = snapshot()
        expected = pending()
        changed = {}
        for table, columns in JSON_COLUMNS.items():
            changed[table] = {}
            for column in columns:
                changed[table][column] = conn.execute(
                    f"UPDATE {table} SET {column}=CAST({column} AS BLOB) "
                    f"WHERE {filters[table]} AND typeof({column})='text'",
                    (PREFIX + "*",)).rowcount
        if changed != expected or snapshot() != before:
            raise RuntimeError("Unexpected row count/content change; rolling back")
        if any(count for columns in pending().values() for count in columns.values()):
            raise RuntimeError("TEXT JSON remains; rolling back")
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    print(encoded({"repaired_columns": changed, "seed_counts": seed_counts(conn),
                   "catalog_values_unchanged": True}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", required=True, type=Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--repair-json-storage", action="store_true",
                        help="Repair existing fixture JSON TEXT to BLOB without inserting rows")
    args = parser.parse_args()
    if not args.db.is_absolute() or not args.db.is_file():
        parser.error("--db must identify an existing database by absolute path")
    db = args.db.resolve()
    # Refuse sensitive configuration paths even when accidentally supplied.
    if any(part.startswith(".env") or part == "config.yml" for part in db.parts):
        parser.error("configuration paths are forbidden")
    categories, products = plan()
    mode = "ro" if args.dry_run else "rw"
    conn = sqlite3.connect(db.as_uri() + f"?mode={mode}", uri=True, timeout=30, isolation_level=None)
    try:
        conn.execute("PRAGMA foreign_keys=ON")
        # Fail closed if these tables have custom triggers with unreviewed effects.
        triggers = conn.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('categories','products','product_skus')").fetchone()[0]
        if triggers:
            raise RuntimeError("Catalog triggers require manual review before seeding")
        if args.repair_json_storage:
            repair_json_storage(conn, db, args.dry_run)
            return
        pending = {"categories": 0, "products": 0, "product_skus": 0}
        for category in categories:
            pending["categories"] += existing(conn, "categories", category["slug"]) is None
        for _, product, skus in products:
            if existing(conn, "products", product["slug"]) is None:
                pending["products"] += 1
                pending["product_skus"] += len(skus)
        print(encoded({"mode": "dry-run" if args.dry_run else "apply", "counts": counts(conn),
                       "plan": {"prefix": PREFIX, "categories": 4, "products": 20, "skus": 32,
                                "guest": 12, "member": 8, "multi_sku": 6, "sold_out": 2,
                                "fulfillment": "manual", "quantity": [1, 5], "pending": pending,
                                "existing_slugs": "skip without updates"}}))
        if args.dry_run:
            return
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
        backup_path = db.with_name(db.name + f".seed-{PREFIX.rstrip('-')}-{stamp}.bak")
        backup_path.touch(mode=0o600, exist_ok=False)
        with sqlite3.connect(backup_path) as backup:
            conn.backup(backup)
        print(encoded({"backup": str(backup_path)}))
        conn.execute("BEGIN IMMEDIATE")
        try:
            before = counts(conn)
            added = {table: 0 for table in before}
            now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f+00:00")
            category_ids = {}
            for category in categories:
                found = existing(conn, "categories", category["slug"])
                if found:
                    if found[1] is not None:
                        raise RuntimeError("Seed category is soft-deleted; refusing to reuse or update it")
                    category_ids[category["slug"]] = found[0]
                else:
                    category_ids[category["slug"]] = insert(conn, "categories", {**category, "created_at": now})
                    added["categories"] += 1
            for category_slug, product, skus in products:
                if existing(conn, "products", product["slug"]):
                    continue
                product_id = insert(conn, "products", {
                    **product, "category_id": category_ids[category_slug], "created_at": now, "updated_at": now,
                })
                added["products"] += 1
                for sku in skus:
                    insert(conn, "product_skus", {**sku, "product_id": product_id, "created_at": now, "updated_at": now})
                    added["product_skus"] += 1
            if conn.execute(VERIFY_SQL, (PREFIX + "*",)).fetchall():
                raise RuntimeError("Seed stock/price aggregation mismatch; rolling back")
            after = counts(conn)
            if any(after[t] - before[t] != added[t] for t in before):
                raise RuntimeError("Unexpected catalog row delta; rolling back")
            conn.execute("COMMIT")
        except BaseException:
            conn.execute("ROLLBACK")
            raise
        ranges = {}
        for table in ("categories", "products"):
            ranges[table] = conn.execute(
                f"SELECT MIN(id),MAX(id),MIN(slug),MAX(slug) FROM {table} WHERE slug GLOB ?",
                (PREFIX + "*",)).fetchone()
        ranges["product_skus"] = conn.execute(
            "SELECT MIN(s.id),MAX(s.id) FROM product_skus s JOIN products p ON p.id=s.product_id WHERE p.slug GLOB ?",
            (PREFIX + "*",)).fetchone()
        print(encoded({"added": added, "seed_counts": seed_counts(conn), "ranges": ranges,
                       "stock_price_mismatches": 0}))
    finally:
        conn.close()


if __name__ == "__main__":
    main()
