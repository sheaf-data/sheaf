TEST(WidgetTest, CreateSucceeds) { EXPECT_EQ(Create("hello"), 0); }
TEST(WidgetTest, CreateRejectsEmpty) { EXPECT_NE(Create(""), 0); }
