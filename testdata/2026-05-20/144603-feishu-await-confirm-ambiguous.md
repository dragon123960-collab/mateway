# 飞书-await_confirm-ambiguous

## 问题
在会话里保留危险操作待确认任务时，发送一个新的独立总结问题，观察 followup 是否会误判为 ambiguous。

## 结果
- 当前输入被判成 ambiguous。
- 原因是会话里有一个待确认的危险删除任务，系统无法判断新输入是继续旧任务还是开启新任务。

## 结论
这说明 await_confirm 任务对后续独立提问的干扰还偏强，需要调整优先级或绑定策略。

## Skills
- stage: synthesis

## 执行过程与参数
- trace_id: feishu-om_x100b6fe134cac880b29a65f831d1ce6
- session_key: feishu:oc_afc228b7734c68f8ea5863fdae50f57a
- channel: feishu
- user_id: local
- thread_id: oc_afc228b7734c68f8ea5863fdae50f57a
- home: /Users/dongping/.mateway
- project_root: /Users/dongping/project/mateway
- generated_at: 2026-05-20T14:46:03+08:00
- trace_file: /Users/dongping/.mateway/trace/events-2026-05-20.jsonl

### Trace Evidence
- runtime.followup_resolved: ambiguous
- reason: 当前输入请求是关于总结Mateway的测试目标，但活跃任务是一个待确认的文件清理操作
