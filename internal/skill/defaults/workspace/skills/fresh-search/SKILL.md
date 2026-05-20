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
