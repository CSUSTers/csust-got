package prom

// func TestA(t *testing.T) {
// 	now := time.Now()
// 	client, err := api.NewClient(api.Config{
// 		Address: "http://192.168.31.15:9090",
// 	})
// 	require.NoError(t, err)
// 	v1api := v1.NewAPI(client)
// 	query := `topk(5, sum(rate(bot_message_count{instance="67.218.140.27:8080", chat_name="$group", is_sticker="true"}[24h])*(24*3600-5)) by (username))`
// 	query = strings.Replace(query, "$group", "Test", -1)
// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()
// 	value, warn, err := v1api.Query(ctx, query, now)
// 	require.NoError(t, err)
// 	for _, e := range warn {
// 		t.Logf(e)
// 	}
// 	vec := value.(model.Vector)
// 	res := make([]MsgCount, 0)
// 	for _, v := range vec {
// 		name := v.Metric.String()
// 		cnt, _ := strconv.ParseFloat(v.Value.String(), 64)
// 		res = append(res, MsgCount{
// 			Name:  name[11 : len(name)-2],
// 			Value: int(cnt),
// 		})
// 	}
// 	t.Logf("%+v", res)
// }
