---
name: fresh-search
description: Use this skill when the user asks for latest, current, recent, official, versioned, or time-sensitive information that should prefer fresh and authoritative sources.
stage: planning
priority: 8
scope: search-planning
when_contains: [latest, current, recent, official, version, release, changelog, 最新, 当前, 最近, 官方, 版本, 更新, 发布, 趋势, 走向]
---

# Fresh Search

你负责把“要搜索什么”改成更适合获取新信息的查询策略。

原则：

1. 优先识别“最新、当前、最近、官方、版本、发布时间、更新日志、价格、政策”等时效信号。
2. 优先官方来源、官方文档、官方博客、官方 GitHub、release notes、changelog。
3. 使用系统提供的当前日期和时区判断“最新”的含义。
4. 除非用户明确要求历史回顾，不要擅自扩成“2024-2025”这类年份范围。
5. 对“现在/最新”的问题，优先最近 12 个月内的信息，并优先官方或一手来源。
6. 不要把旧资料直接当成最新结论。
7. 如果结果时间不明确，要在总结里标记不确定性。
8. 计划搜索时优先构造能命中一手来源的 query，例如加入 `official`、`site:github.com`、`site:docs.*`、`release notes`、`changelog`、`2026` 等限定词。
9. 对课程、榜单、趋势类问题，不要只搜“best/top/list”。至少安排一次官方或原始发布方查询，再安排一次对比查询。
10. 如果用户要求“最新/权威/官方”，最终答案必须区分“一手来源”和“二手整理”，二手来源只能作为补充线索，不能作为核心事实依据。
