export interface StoryTemplate {
  id: string;
  label: string;
  badge: string;
  title: string;
  aspect_ratio: "16:9" | "9:16" | "1:1";
  target_frames: number;
  style_preset: string;
  raw_story: string;
}

export const DEMO_TEMPLATES: StoryTemplate[] = [
  {
    id: "beijing_2016_overpass",
    label: "2016 北京冬夜过街天桥",
    badge: "情感叙事 · 4帧",
    title: "2016 北京冬夜过街天桥",
    aspect_ratio: "16:9",
    target_frames: 4,
    style_preset: "cinematic_melancholy",
    raw_story:
      "2016年冬夜十一点，林深穿着黑色羊毛长风衣站在北京三环过街天桥上。细雪和冷雨打湿了风衣肩头，他举着一把黑色长柄伞，仰望深紫色夜空和天桥下昏黄与霓虹交错的车流尾灯。第二刻，汽车远光灯掠过天桥栏杆，林深收起黑伞搭在左手腕上，低头看了一眼腕间发旧的银色手表。第三刻，他独自走下湿滑结霜的天桥铁楼梯，风衣下摆被冷风吹起。最后，他停在天桥出口的路灯暖光下，将一只银色随身酒壶握在手心，转身最后看了一眼空荡的桥面。",
  },
  {
    id: "rainy_bookstore_lamp",
    label: "雨夜旧书店的最后一盏灯",
    badge: "双人互动 · 4帧",
    title: "雨夜旧书店的最后一盏灯",
    aspect_ratio: "16:9",
    target_frames: 4,
    style_preset: "warm_film_grain",
    raw_story:
      "秋雨拍打着巷尾旧书店的玻璃橱窗，店主老周戴着铜框老花镜、穿一件深棕色灯芯绒马甲，在温暖台灯下整理一摞泛黄诗集。门铃叮当一响，浑身湿透、背着墨绿色帆布包的青年女孩推门而入，将滴水的透明折伞放在门边铜桶里。老周递给她一条干燥的米白色棉毛巾和一杯冒着热气的红茶。女孩接过茶杯，在堆满旧书的木桌旁指着一页手写书签轻声微笑。",
  },
  {
    id: "neon_subway_watch",
    label: "霓虹地铁末班车与银色怀表",
    badge: "竖屏赛博 · 4帧",
    title: "霓虹地铁末班车与银色怀表",
    aspect_ratio: "9:16",
    target_frames: 4,
    style_preset: "anime_cyber_noir",
    raw_story:
      "午夜零点的地下换乘站泛着青蓝与品红荧光，穿炭灰机能短夹克、戴半指黑手套的信使站在空旷站台白线后，右手紧握一块泛冷光的刻花银色怀表。末班银色列车的车灯从隧道深处刺破薄雾呼啸进站，反光映照在他冷静的瞳孔与发梢。车门滑开，他将怀表收入胸前暗袋，迎着车厢暖白色灯光迈步踏入列车。车窗外飞驰而过的广告牌霓虹与倒影中，他静静靠窗站立。",
  },
];
