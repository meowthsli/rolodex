import json
import os
import sqlite3
from pathlib import Path
from urllib.parse import urlparse

import pandas as pd
import streamlit as st

DB_PATH = Path(os.environ.get("ROLODEX_DB", "rolodex.db")).resolve()


@st.cache_resource
def connect() -> sqlite3.Connection:
    conn = sqlite3.connect(DB_PATH, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON;")
    return conn


# Maps a UI metric label to the underlying table and WHERE filter.
_COUNTS = {
    "entities": ("entities", "1=1"),
    "relations": ("relations", "1=1"),
    "passes": ("passes", "1=1"),
    "links": ("link_queue", "last_scrapped_at IS NOT NULL"),
    "pending": ("link_queue", "last_scrapped_at IS NULL"),
}


@st.cache_data(ttl=10)
def load_count(section: str) -> int:
    table, where = _COUNTS[section]
    conn = connect()
    cur = conn.execute(f"SELECT COUNT(*) FROM {table} WHERE {where}")
    return cur.fetchone()[0]


def load_entities() -> pd.DataFrame:
    conn = connect()
    q = """
        SELECT id, display_name AS name, types, promotion_score AS score,
               is_known AS known, created_at
        FROM entities
        ORDER BY promotion_score DESC, display_name
    """
    df = pd.read_sql_query(q, conn)
    df["types"] = df["types"].map(_parse_types)
    df["known"] = df["known"].astype(bool)
    return df


def _parse_types(raw: str) -> str:
    try:
        vals = json.loads(raw)
        return ", ".join(vals) if vals else ""
    except Exception:
        return raw


def load_relations() -> pd.DataFrame:
    conn = connect()
    q = """
        SELECT r.id, s.display_name AS source, r.type, t.display_name AS target,
               r.confidence, r.chunk_index, r.created_at
        FROM relations r
        JOIN entities s ON s.id = r.source_id
        JOIN entities t ON t.id = r.target_id
        ORDER BY r.created_at DESC
    """
    return pd.read_sql_query(q, conn)


def load_entity_detail(entity_id: int) -> dict:
    conn = connect()
    ent = conn.execute(
        "SELECT * FROM entities WHERE id = ?", (entity_id,)
    ).fetchone()
    if ent is None:
        return {}
    aliases = [
        r["alias"]
        for r in conn.execute(
            "SELECT alias FROM entity_aliases WHERE entity_id = ? ORDER BY alias",
            (entity_id,),
        )
    ]
    out = []
    target = []
    rows = conn.execute(
        """
        SELECT r.id, r.type, r.confidence, s.display_name AS other,
               t.display_name AS target_name
        FROM relations r
        JOIN entities s ON s.id = r.source_id
        JOIN entities t ON t.id = r.target_id
        WHERE r.source_id = ? OR r.target_id = ?
        ORDER BY r.created_at DESC
        """,
        (entity_id, entity_id),
    ).fetchall()
    for r in rows:
        if r["other"] and r["other"] == ent["display_name"]:
            out.append(
                {"Rel": r["type"], "Entity": r["target_name"], "Direction": "← in"}
            )
        else:
            out.append(
                {"Rel": r["type"], "Entity": r["other"], "Direction": "→ out"}
            )
    return {
        "entity": ent,
        "aliases": aliases,
        "relations": pd.DataFrame(out) if out else pd.DataFrame(
            columns=["Rel", "Entity", "Direction"]
        ),
    }


def load_passes() -> pd.DataFrame:
    conn = connect()
    q = """
        SELECT p.id, p.domain, p.chunk_index, length(p.chunk_text) AS text_len,
               p.error, p.created_at, p.extracted_at, l.url
        FROM passes p
        LEFT JOIN link_queue l ON l.id = p.link_queue_id
        ORDER BY p.created_at DESC
    """
    df = pd.read_sql_query(q, conn)
    df["domain"] = df["domain"].replace("", "?")
    return df


def load_links() -> pd.DataFrame:
    conn = connect()
    q = """
        SELECT id, url, generation,
               last_scrapped_at, error, added_at
        FROM link_queue
        ORDER BY added_at DESC
    """
    df = pd.read_sql_query(q, conn)
    df["domain"] = df["url"].map(lambda u: urlparse(u).netloc) # type: ignore
    df["status"] = df["error"].where(df["error"].notna(), "ok")
    return df


def load_profiles() -> pd.DataFrame:
    conn = connect()
    q = """
        SELECT ep.entity_id AS id, e.display_name AS name, ep.profile,
               ep.updated_at
        FROM entity_profiles ep
        JOIN entities e ON e.id = ep.entity_id
        WHERE NOT EXISTS (
            SELECT 1 FROM json_each(e.types)
            WHERE json_each.value IN ('Date', 'Product')
        )
        ORDER BY e.display_name
    """
    return pd.read_sql_query(q, conn)


st.set_page_config(page_title="Rolodex", layout="wide")

st.title("Rolodex")
st.caption(f"Read-only view of `{DB_PATH}`")

c1, c2, c3, c4, c5 = st.columns(5)
c1.metric("Entities", load_count("entities"))
c2.metric("Relations", load_count("relations"))
c3.metric("Passes", load_count("passes"))
c4.metric("Links scraped", load_count("links"))
c5.metric("Links pending", load_count("pending"))

tab_entities, tab_relations, tab_graph, tab_profiles, tab_passes, tab_links = st.tabs(
    ["Entities", "Relations", "Graph", "Profiles", "Passes", "Links"]
)

with tab_entities:
    st.subheader("Entities")
    df_ent = load_entities()
    st.dataframe(df_ent, width='stretch', hide_index=True)
    selected = st.selectbox(
        "Entity detail",
        df_ent["name"].tolist(),
        index=None,
        placeholder="Choose an entity…",
    )
    if selected:
        row = df_ent[df_ent["name"] == selected].iloc[0]
        detail = load_entity_detail(int(row["id"]))
        st.markdown(f"### {row['name']}")
        st.write("Types:", row["types"])
        st.write("Promotion score:", row["score"])
        st.write("Known:", row["known"])
        if detail["aliases"]:
            st.write("Aliases:", ", ".join(detail["aliases"]))
        st.dataframe(
            detail["relations"],
            width='stretch',
            hide_index=True,
        )

with tab_relations:
    st.subheader("Relations")
    st.dataframe(load_relations(), width='stretch', hide_index=True)

with tab_graph:
    st.subheader("Graph")
    try:
        import networkx as nx
        import matplotlib.pyplot as plt
    except ImportError:
        st.info("Install `networkx` and `matplotlib` to render the graph:")
        st.code("venv/bin/pip install networkx matplotlib", language="bash")
    else:
        top = st.slider(
            "Top entities by promotion score",
            min_value=10,
            max_value=min(71, 70),
            value=30,
        )
        conn = connect()
        top_ids = [
            r[0]
            for r in conn.execute(
                "SELECT id FROM entities ORDER BY promotion_score DESC LIMIT ?",
                (top,),
            )
        ]
        placeholders = ",".join("?" for _ in top_ids)
        rel_rows = conn.execute(
            f"""
            SELECT s.display_name AS a, r.type, t.display_name AS b
            FROM relations r
            JOIN entities s ON s.id = r.source_id
            JOIN entities t ON t.id = r.target_id
            WHERE s.id IN ({placeholders}) AND t.id IN ({placeholders})
            """,
            top_ids + top_ids,
        ).fetchall()
        G = nx.DiGraph()
        for r in rel_rows:
            G.add_edge(r["a"], r["b"], label=r["type"])

        # Entity selector: pick a node to focus on and center in the view. When
        # none is chosen ("full graph"), spring_layout decides the layout.
        focus = st.selectbox(
            "Center on entity",
            ["(full graph)"] + sorted(G.nodes()),
        )

        fig, ax = plt.subplots(figsize=(12, 12))
        if focus == "(full graph)":
            pos = nx.spring_layout(G, seed=7)
            node_color = "#4c7bbb"
            node_sizes = [500] * len(G)
            draw_nodes = G
        else:
            # Restrict to the focused node plus its 1-hop neighbors. Layout is
            # computed on the full graph so the picture stays stable, then the
            # focus node is translated to the origin (dead center).
            draw_nodes = G.subgraph(
                {focus} | set(G.predecessors(focus)) | set(G.successors(focus))
            ).copy()
            pos = nx.spring_layout(G, seed=7)
            dx, dy = pos[focus]
            pos = {n: (x - dx, y - dy) for n, (x, y) in pos.items()
                   if n in draw_nodes}
            node_color = [
                "#e07b39" if n == focus else "#4c7bbb" for n in draw_nodes.nodes()
            ]
            node_sizes = [
                1200 if n == focus else 500 for n in draw_nodes.nodes()
            ]
        nx.draw_networkx(draw_nodes, pos, ax=ax, with_labels=True,
                         node_color=node_color, node_size=node_sizes,
                         font_size=8, arrows=True, arrowstyle="-|>")
        st.pyplot(fig)

with tab_profiles:
    st.subheader("Entity profiles")
    df_prof = load_profiles()
    if df_prof.empty:
        st.info(
            "No profiles yet. Build them with `make profiles` "
            "(the rolodex service also rebuilds a profile on every entity event)."
        )
    else:
        name = st.selectbox(
            "Profile",
            df_prof["name"].tolist(),
            index=None,
            placeholder="Choose an entity profile…",
        )
        st.caption(f"{len(df_prof)} profiles stored")
        if name:
            row = df_prof[df_prof["name"] == name].iloc[0]
            st.caption(f"Updated {row['updated_at']}")
            st.markdown(row["profile"], unsafe_allow_html=True)

with tab_passes:
    st.subheader("Passes")
    st.dataframe(load_passes(), width='stretch', hide_index=True)

with tab_links:
    st.subheader("Link queue")
    st.dataframe(load_links(), width='stretch', hide_index=True)
